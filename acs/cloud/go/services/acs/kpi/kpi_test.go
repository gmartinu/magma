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

package kpi_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/kpi"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/sqorc"
	"magma/orc8r/lib/go/protos"
)

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }

func values(ms []*protos.PushedMetric) map[string]float64 {
	out := map[string]float64{}
	for _, m := range ms {
		out[m.MetricName+"/"+m.Labels[0].Value] = m.Value
	}
	return out
}

func TestSamplesSkipMissingValues(t *testing.T) {
	clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	defer clock.UnfreezeClock(t)

	m := &datamodel.Model{UptimeSec: i(3600)}
	m.Cellular.RSRP = f(-95.5)
	m.Cellular.SINR = f(0)
	got := kpi.Samples("D-1", m)
	assert.Equal(t, map[string]float64{"acs_rsrp_dbm/D-1": -95.5, "acs_sinr_db/D-1": 0, "acs_uptime_s/D-1": 3600}, values(got))
	for _, s := range got {
		assert.Equal(t, int64(1000000), s.TimestampMS)
		assert.Equal(t, []*protos.LabelPair{{Name: kpi.CPELabel, Value: "D-1"}}, s.Labels)
	}
	assert.Empty(t, kpi.Samples("D-1", &datamodel.Model{}))
	assert.Empty(t, kpi.Samples("D-1", nil))
}

// FakePusher records pushes on a channel.
type FakePusher struct {
	Pushed chan Push
	Err    error
	Block  chan struct{}
}

type Push struct {
	NetworkID string
	Metrics   []*protos.PushedMetric
}

func (p *FakePusher) Push(_ context.Context, networkID string, metrics []*protos.PushedMetric) error {
	if p.Block != nil {
		<-p.Block
	}
	p.Pushed <- Push{networkID, metrics}
	return p.Err
}

func TestReporter(t *testing.T) {
	p := &FakePusher{Pushed: make(chan Push, 10)}
	r := kpi.NewReporter(p, time.Second, 4)
	sample := kpi.Sample(kpi.Online, "D-1", 1)

	r.Report("", []*protos.PushedMetric{sample})
	r.Report("net", nil)
	r.Report("net", []*protos.PushedMetric{sample})
	got := <-p.Pushed
	assert.Equal(t, "net", got.NetworkID)
	assert.Equal(t, []*protos.PushedMetric{sample}, got.Metrics)
	assert.Empty(t, p.Pushed, "unclaimed and empty reports are not pushed")

	var nilReporter *kpi.Reporter
	nilReporter.Report("net", []*protos.PushedMetric{sample})

	failures := testutil.ToFloat64(kpi.PushFailures)
	p.Err = errors.New("metricsd down")
	r.Report("net", []*protos.PushedMetric{sample})
	<-p.Pushed
	assert.Eventually(t, func() bool { return testutil.ToFloat64(kpi.PushFailures) == failures+1 }, time.Second, 5*time.Millisecond)
}

func TestReporterDropsWhenSaturated(t *testing.T) {
	p := &FakePusher{Pushed: make(chan Push, 10), Block: make(chan struct{})}
	r := kpi.NewReporter(p, time.Second, 1)
	dropped := testutil.ToFloat64(kpi.PushesDropped)
	sample := []*protos.PushedMetric{kpi.Sample(kpi.Online, "D-1", 1)}
	r.Report("net", sample)
	r.Report("net", sample)
	assert.Equal(t, dropped+1, testutil.ToFloat64(kpi.PushesDropped))
	close(p.Block)
	<-p.Pushed
}

func TestReportOnline(t *testing.T) {
	clock.SetAndFreezeClock(t, time.Unix(10000, 0))
	defer clock.UnfreezeClock(t)
	db, err := sqorc.Open("sqlite3", ":memory:?_foreign_keys=1")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.Close()
	store := storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder())
	require.NoError(t, store.Init())
	for _, d := range []*storage.Device{
		{DeviceID: "A-1", OUI: "A", SerialNumber: "1", LastSeenSec: 9900, InformIntervalSec: 100},
		{DeviceID: "A-2", OUI: "A", SerialNumber: "2", LastSeenSec: 9000, InformIntervalSec: 100},
		{DeviceID: "B-1", OUI: "B", SerialNumber: "1", LastSeenSec: 9999},
		{DeviceID: "U-1", OUI: "U", SerialNumber: "1", LastSeenSec: 9999},
	} {
		require.NoError(t, store.UpsertDevice(d))
	}
	require.NoError(t, store.ClaimDevice("A-1", "net-a"))
	require.NoError(t, store.ClaimDevice("A-2", "net-a"))
	require.NoError(t, store.ClaimDevice("B-1", "net-b"))

	p := &FakePusher{Pushed: make(chan Push, 10)}
	require.NoError(t, kpi.NewReporter(p, time.Second, 4).ReportOnline(store, storage.DefaultOnlinePolicy))
	byNetwork := map[string]map[string]float64{}
	for n := 0; n < 2; n++ {
		got := <-p.Pushed
		byNetwork[got.NetworkID] = values(got.Metrics)
	}
	assert.Equal(t, map[string]map[string]float64{
		"net-a": {"acs_online/A-1": 1, "acs_online/A-2": 0},
		"net-b": {"acs_online/B-1": 1},
	}, byNetwork)
}
