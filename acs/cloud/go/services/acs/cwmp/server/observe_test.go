/*
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/cwmp/simulator"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/kpi"
	"magma/acs/cloud/go/services/acs/sessionlog"
	"magma/acs/cloud/go/services/acs/tasks"
	"magma/orc8r/lib/go/protos"
)

type push struct {
	networkID string
	values    map[string]float64
}

type fakePusher struct {
	pushed chan push
}

func (p *fakePusher) Push(_ context.Context, networkID string, metrics []*protos.PushedMetric) error {
	values := map[string]float64{}
	for _, m := range metrics {
		values[m.MetricName] = m.Value
	}
	p.pushed <- push{networkID, values}
	return nil
}

func (p *fakePusher) next(t *testing.T) push {
	select {
	case got := <-p.pushed:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("no metrics pushed")
		return push{}
	}
}

type memSink struct {
	mu      sync.Mutex
	records []*sessionlog.Record
}

func (s *memSink) Emit(r *sessionlog.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, r)
}

// take returns the records emitted since the last call.
func (s *memSink) take() []*sessionlog.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.records
	s.records = nil
	return out
}

func events(records []*sessionlog.Record) []string {
	out := []string{}
	for _, r := range records {
		e := r.Event
		if r.RPC != "" {
			e += " " + r.RPC
		}
		out = append(out, e)
	}
	return out
}

func TestKPIsAndSessionLogs(t *testing.T) {
	pusher := &fakePusher{pushed: make(chan push, 10)}
	logs := &memSink{}
	h := newHarnessOn(t, openSQLite(t), defaultConfig(), 2,
		server.WithKPIReporter(kpi.NewReporter(pusher, time.Second, 4)), server.WithSessionLog(logs))
	cpe := h.cpe(datamodel.RootTR181)
	sessions := testutil.ToFloat64(kpi.Sessions)

	// Unclaimed: logged without a network, nothing pushed.
	runSession(t, cpe, cwmp.EventBootstrap)
	recs := logs.take()
	assert.Equal(t, []string{"inform", "rpc SetParameterValues", "session_end"}, events(recs))
	inform := recs[0]
	assert.Equal(t, h.deviceID(), inform.DeviceID)
	assert.Empty(t, inform.NetworkID)
	assert.Equal(t, "127.0.0.1", inform.SourceIP)
	assert.Equal(t, []string{cwmp.EventBootstrap}, inform.EventCodes)
	assert.True(t, inform.Bootstrap)
	assert.NotEmpty(t, inform.SessionID)
	assert.NotZero(t, inform.TimestampMs)
	for _, r := range recs {
		assert.Equal(t, inform.SessionID, r.SessionID)
	}
	assert.Equal(t, 1, recs[2].RPCCount)
	assert.Empty(t, pusher.pushed)

	// Claimed, with radio values in the Inform: SINR is not reported, so it
	// has no sample.
	require.NoError(t, h.store.ClaimDevice(h.deviceID(), "net1"))
	cpe.Params["Device.Cellular.Interface.1.RSRP"] = simulator.Param{Value: "-101", Type: "xsd:int"}
	cpe.InformParams = append(cpe.InformParams,
		"Device.Cellular.Interface.1.RSRP", "Device.Cellular.Interface.1.RSRQ", "Device.DeviceInfo.UpTime")
	runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, push{"net1", map[string]float64{
		kpi.RSRP: -101, kpi.RSRQ: -11, kpi.Uptime: 100, kpi.Online: 1, kpi.InformTotal: 2,
	}}, pusher.next(t))
	recs = logs.take()
	assert.Equal(t, []string{"inform", "session_end"}, events(recs))
	assert.Equal(t, "net1", recs[0].NetworkID)
	assert.Equal(t, "net1", recs[1].NetworkID)

	// A refresh pushes what it read, SINR included; a CPE fault is logged
	// with its task and counted by code.
	faults := testutil.ToFloat64(kpi.Faults.WithLabelValues("9005"))
	refresh := h.queue(tasks.TypeRefresh, tasks.Args{}, 3)
	missing := h.queue(tasks.TypeGetParameterValues, tasks.Args{ParameterNames: []string{"Device.Nope"}}, 3)
	runSession(t, cpe, cwmp.EventPeriodic)
	assert.Contains(t, pusher.next(t).values, kpi.InformTotal, "the Inform comes first")
	got := pusher.next(t)
	assert.Equal(t, "net1", got.networkID)
	assert.Equal(t, map[string]float64{kpi.RSRP: -101, kpi.RSRQ: -11, kpi.SINR: 14, kpi.Uptime: 100}, got.values)
	recs = logs.take()
	var faulted *sessionlog.Record
	for _, r := range recs {
		if r.TaskID == missing {
			faulted = r
		}
		if r.TaskID == refresh {
			assert.Equal(t, tasks.TypeRefresh, r.TaskType)
		}
	}
	require.NotNil(t, faulted)
	assert.Equal(t, "GetParameterValues", faulted.RPC)
	assert.Equal(t, tasks.TypeGetParameterValues, faulted.TaskType)
	assert.Equal(t, int(cwmp.FaultCPEInvalidParamName), faulted.FaultCode)
	assert.Equal(t, faults+1, testutil.ToFloat64(kpi.Faults.WithLabelValues("9005")))
	end := recs[len(recs)-1]
	assert.Equal(t, sessionlog.EventSessionEnd, end.Event)
	assert.Equal(t, len(recs)-2, end.RPCCount)
	assert.Equal(t, sessions+3, testutil.ToFloat64(kpi.Sessions))

	// A bad bootstrap password is an auth failure.
	authFailures := testutil.ToFloat64(kpi.AuthFailures)
	other := simID
	other.SerialNumber = "SIM0002"
	intruder := simulator.New(h.ts.URL+"/acs", datamodel.RootTR181, other, bootstrapUser, "wrong")
	s, err := intruder.RunSession(cwmp.EventBootstrap)
	if err == nil {
		assert.Equal(t, http.StatusUnauthorized, s.Status)
	}
	assert.Equal(t, authFailures+1, testutil.ToFloat64(kpi.AuthFailures))
	recs = logs.take()
	require.Len(t, recs, 1)
	assert.Equal(t, sessionlog.EventAuthFailure, recs[0].Event)
	assert.Equal(t, other.DeviceID(), recs[0].DeviceID)
	assert.NotEmpty(t, recs[0].Reason)
}

func TestInformRateLimitObserved(t *testing.T) {
	logs := &memSink{}
	cfg := defaultConfig()
	cfg.InformRateLimit = 1
	h := newHarnessOn(t, openSQLite(t), cfg, 1, server.WithSessionLog(logs))
	cpe := h.cpe(datamodel.RootTR181)
	limited := testutil.ToFloat64(kpi.RateLimited)
	runSession(t, cpe, cwmp.EventBootstrap)
	_, err := cpe.RunSession(cwmp.EventPeriodic)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "503")
	assert.Equal(t, limited+1, testutil.ToFloat64(kpi.RateLimited))
	recs := logs.take()
	assert.Equal(t, sessionlog.EventRateLimited, recs[len(recs)-1].Event)
}
