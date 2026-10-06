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

package reports_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/acs/cloud/go/services/acs/reports"
	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/services/state/protos"
	state_types "magma/orc8r/cloud/go/services/state/types"
	"magma/orc8r/cloud/go/test_utils"
	lib_protos "magma/orc8r/lib/go/protos"
)

func view(key string, lastInform float64) *cpestate.CpeView {
	return &cpestate.CpeView{CpeKey: key, Mode: cpestate.ModeCore, LastInform: lastInform}
}

func newStore(t *testing.T) *reports.Store {
	factory := test_utils.NewSQLBlobstore(t, "acs_reports_test_"+t.Name())
	require.NoError(t, factory.InitializeFactory())
	return reports.NewStore(factory)
}

func TestStoreKeepsOneReportPerGateway(t *testing.T) {
	s := newStore(t)
	require.NoError(t, s.Put("n1", []reports.Report{
		{HardwareID: "hw1", TimeMs: 10, View: view("IMSI1", 100)},
		{HardwareID: "hw2", TimeMs: 20, View: view("IMSI1", 200)},
		{HardwareID: "hw1", TimeMs: 10, View: view("IMSI2", 100)},
	}))
	require.NoError(t, s.Put("n2", []reports.Report{{HardwareID: "hw9", TimeMs: 1, View: view("IMSI1", 1)}}))
	// A newer report of hw1 replaces its older one.
	require.NoError(t, s.Put("n1", []reports.Report{{HardwareID: "hw1", TimeMs: 30, View: view("IMSI1", 150)}}))

	got, err := s.Get("n1", "IMSI1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	byHw := map[string]reports.Report{}
	for _, r := range got {
		byHw[r.HardwareID] = r
	}
	assert.Equal(t, uint64(30), byHw["hw1"].TimeMs)
	assert.Equal(t, 150.0, byHw["hw1"].View.LastInform)
	assert.Equal(t, 200.0, byHw["hw2"].View.LastInform)

	all, err := s.List("n1")
	require.NoError(t, err)
	assert.Len(t, all, 2)
	assert.Len(t, all["IMSI2"], 1)

	require.NoError(t, s.Delete("n1", "IMSI1", "hw2"))
	got, err = s.Get("n1", "IMSI1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "hw1", got[0].HardwareID)

	got, err = s.Get("n1", "IMSI9")
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestResolve(t *testing.T) {
	stale := reports.Report{HardwareID: "hw1", TimeMs: 50, View: view("IMSI1", 100)}
	serving := reports.Report{HardwareID: "hw2", TimeMs: 40, View: view("IMSI1", 200)}
	owner, others := reports.Resolve([]reports.Report{stale, serving})
	assert.Equal(t, "hw2", owner.HardwareID, "newest Inform wins over newest report")
	assert.Equal(t, []reports.Report{stale}, others)

	// Same Inform: newest report, then lowest hardware ID.
	a := reports.Report{HardwareID: "hwB", TimeMs: 10, View: view("IMSI1", 0)}
	b := reports.Report{HardwareID: "hwA", TimeMs: 10, View: view("IMSI1", 0)}
	c := reports.Report{HardwareID: "hwC", TimeMs: 11, View: view("IMSI1", 0)}
	owner, _ = reports.Resolve([]reports.Report{a, b, c})
	assert.Equal(t, "hwC", owner.HardwareID)
	owner, _ = reports.Resolve([]reports.Report{a, b})
	assert.Equal(t, "hwA", owner.HardwareID)
	owner, _ = reports.Resolve([]reports.Report{b, a})
	assert.Equal(t, "hwA", owner.HardwareID)
}

func protoState(t *testing.T, hwID string, timeMs uint64, v *cpestate.CpeView) *lib_protos.State {
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	p, err := state_types.MakeProtoState(
		state_types.ID{Type: lte.CPEAcsStateType, DeviceID: v.CpeKey},
		state_types.SerializedState{SerializedReportedState: raw, ReporterID: hwID, TimeMs: timeMs},
	)
	require.NoError(t, err)
	return p
}

func TestIndexer(t *testing.T) {
	s := newStore(t)
	idx := reports.NewIndexerServicer(s)
	ctx := context.Background()

	_, err := idx.Index(ctx, &protos.IndexRequest{NetworkId: "n1", States: []*lib_protos.State{protoState(t, "hw1", 10, view("IMSI1", 100))}})
	require.NoError(t, err)
	_, err = idx.Index(ctx, &protos.IndexRequest{NetworkId: "n1", States: []*lib_protos.State{protoState(t, "hw2", 20, view("IMSI1", 200))}})
	require.NoError(t, err)

	got, err := s.Get("n1", "IMSI1")
	require.NoError(t, err)
	require.Len(t, got, 2, "the second gateway's report must not replace the first's")
	owner, _ := reports.Resolve(got)
	assert.Equal(t, "hw2", owner.HardwareID)
	assert.Equal(t, uint64(20), owner.TimeMs)

	_, err = idx.DeIndex(ctx, &protos.DeIndexRequest{NetworkId: "n1", States: []*lib_protos.State{protoState(t, "hw2", 20, view("IMSI1", 200))}})
	require.NoError(t, err)
	got, err = s.Get("n1", "IMSI1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "hw1", got[0].HardwareID)
}

func TestIndexerReindexVersions(t *testing.T) {
	idx := reports.NewIndexerServicer(newStore(t))
	_, err := idx.CompleteReindex(context.Background(), &protos.CompleteReindexRequest{FromVersion: 0, ToVersion: uint32(reports.IndexerVersion)})
	assert.NoError(t, err)
	_, err = idx.CompleteReindex(context.Background(), &protos.CompleteReindexRequest{FromVersion: 1, ToVersion: 7})
	assert.Error(t, err)
}
