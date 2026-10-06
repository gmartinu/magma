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

package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/reports"
	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/configurator"
	configurator_test_init "magma/orc8r/cloud/go/services/configurator/test_init"
	device_test_init "magma/orc8r/cloud/go/services/device/test_init"
	entitlements_servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	entitlements_test_init "magma/orc8r/cloud/go/services/entitlements/test_init"
	"magma/orc8r/cloud/go/services/obsidian"
	state_protos "magma/orc8r/cloud/go/services/state/protos"
	state_types "magma/orc8r/cloud/go/services/state/types"
	"magma/orc8r/cloud/go/test_utils"
	lib_protos "magma/orc8r/lib/go/protos"
)

// testReports is the CPE report store of the running test; setupNetwork
// makes a new one.
var testReports *reports.Store

func newHandlers(cpes CpeManagers) *Handlers {
	return NewHandlers(cpes, testReports)
}

// setupNetwork starts the services the handlers read, with entitlements
// not enforced, and registers g1/hw1 and g2/hw2 in n1, so both gateways
// can report state.
func setupNetwork(t *testing.T) {
	setupNetworkWithEntitlements(t, entitlements_servicers.Config{Enforce: false})
}

func setupNetworkWithEntitlements(t *testing.T, cfg entitlements_servicers.Config) {
	entitlements_test_init.StartTestService(t, cfg, nil)
	configurator_test_init.StartTestService(t)
	device_test_init.StartTestService(t)
	factory := test_utils.NewSQLBlobstore(t, "acs_handlers_test_"+strings.NewReplacer("/", "_", "-", "_").Replace(t.Name()))
	require.NoError(t, factory.InitializeFactory())
	testReports = reports.NewStore(factory)

	ctx := context.Background()
	require.NoError(t, configurator.CreateNetwork(ctx, configurator.Network{ID: "n1", Type: lte.NetworkType}, nil))
	_, err := configurator.CreateEntities(ctx, "n1", []configurator.NetworkEntity{
		{Type: orc8r.MagmadGatewayType, Key: "g1", PhysicalID: "hw1"},
		{Type: orc8r.MagmadGatewayType, Key: "g2", PhysicalID: "hw2"},
	}, nil)
	require.NoError(t, err)
}

// reportCpe is hwID's acsd reporting view now, as the state service hands
// it to the acs indexer.
func reportCpe(t *testing.T, hwID string, view *cpestate.CpeView) {
	reportCpeAt(t, hwID, view, time.Now())
}

func reportCpeAt(t *testing.T, hwID string, view *cpestate.CpeView, at time.Time) {
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	st, err := state_types.MakeProtoState(
		state_types.ID{Type: lte.CPEAcsStateType, DeviceID: view.CpeKey},
		state_types.SerializedState{SerializedReportedState: raw, ReporterID: hwID, TimeMs: uint64(at.UnixMilli())},
	)
	require.NoError(t, err)
	_, err = reports.NewIndexerServicer(testReports).Index(context.Background(), &state_protos.IndexRequest{NetworkId: "n1", States: []*lib_protos.State{st}})
	require.NoError(t, err)
}

// serve runs the route registered for path, so the requireFeature wrapper
// is part of every test.
func serve(t *testing.T, h *Handlers, method, path, url string, params map[string]string, body string) *httptest.ResponseRecorder {
	for _, hd := range h.GetHandlers() {
		if hd.Path != path || hd.Methods != methods[method] {
			continue
		}
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		names, values := []string{}, []string{}
		for k, v := range params {
			names, values = append(names, k), append(values, v)
		}
		c.SetParamNames(names...)
		c.SetParamValues(values...)
		if err := hd.HandlerFunc(c); err != nil {
			httpErr, ok := err.(*echo.HTTPError)
			require.True(t, ok, "unexpected error %v", err)
			rec.Code = httpErr.Code
			_ = json.NewEncoder(rec.Body).Encode(httpErr.Message)
		}
		return rec
	}
	t.Fatalf("no %s handler for %s", method, path)
	return nil
}

var methods = map[string]obsidian.HttpMethod{http.MethodGet: obsidian.GET, http.MethodPost: obsidian.POST}

func listCpes(t *testing.T, h *Handlers, query string) []models.AcsCpe {
	rec := serve(t, h, http.MethodGet, CpesPath, "/magma/v1/acs/n1/cpes"+query, map[string]string{"network_id": "n1"}, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out []models.AcsCpe
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

func keys(cpes []models.AcsCpe) []string {
	out := []string{}
	for _, c := range cpes {
		out = append(out, c.CpeKey)
	}
	return out
}

// informAt is the last Inform of the test CPEs: recent, so the CPE list
// does not take them as stale.
var informAt = time.Now().Truncate(time.Second).Add(-time.Minute)

func titan(key, mode string, online bool) *cpestate.CpeView {
	return &cpestate.CpeView{
		CpeKey: key, Mode: mode, Imsi: key, SerialNumber: "SN-" + key, Oui: "00259E",
		ProductClass: "Titan4000", SoftwareVersion: "1.2.3", Handler: "titan",
		LastInform: float64(informAt.Unix()) + 0.5, InformsTotal: 7, Online: online, PendingTasks: 1,
		LastSession: &cpestate.CpeSession{SessionID: "s1", Result: "completed", Started: 1700000000, Ended: 1700000001, TasksDone: 2},
		Model:       map[string]interface{}{"identity": map[string]interface{}{"model_name": "Titan 4000"}},
		Reach:       cpestate.ReachConnectionRequest,
		NextInform:  float64(informAt.Unix()) + 300.5,
	}
}

func TestListCpes(t *testing.T) {
	setupNetwork(t)
	h := newHandlers(nil)

	assert.Empty(t, listCpes(t, h, ""))

	reportCpe(t, "hw1", titan("IMSI001010000000001", cpestate.ModeCore, true))
	reportCpe(t, "hw1", titan("IMSI001010000000002", cpestate.ModeCore, false))
	fastmile := &cpestate.CpeView{CpeKey: "CLAIM7", Mode: cpestate.ModeClaimed, ProductClass: "5G16-A", Online: true}
	reportCpe(t, "hw2", fastmile)

	all := listCpes(t, h, "")
	require.Equal(t, []string{"CLAIM7", "IMSI001010000000001", "IMSI001010000000002"}, keys(all))

	got := all[1]
	assert.Equal(t, "g1", got.GatewayID)
	assert.Equal(t, "hw1", got.HardwareID)
	assert.Equal(t, "core", got.Mode)
	assert.Equal(t, "Titan 4000", got.ModelName)
	assert.Equal(t, "SN-IMSI001010000000001", got.SerialNumber)
	assert.Equal(t, int64(7), got.InformsTotal)
	assert.Equal(t, int32(1), got.PendingTasks)
	assert.True(t, got.Online)
	require.NotNil(t, got.LastInform)
	assert.Equal(t, informAt.Add(500*time.Millisecond).UTC(), time.Time(*got.LastInform))
	require.NotNil(t, got.LastSession)
	assert.Equal(t, "completed", got.LastSession.Result)
	assert.Equal(t, int32(2), got.LastSession.TasksDone)
	assert.WithinDuration(t, time.Now(), time.Time(got.ReportedAt), time.Minute)
	assert.Nil(t, all[0].LastInform, "never-set times are absent")
	assert.Nil(t, all[0].NextInform)

	assert.Equal(t, []string{"IMSI001010000000001", "IMSI001010000000002"}, keys(listCpes(t, h, "?model=Titan+4000")))
	assert.Equal(t, []string{"CLAIM7"}, keys(listCpes(t, h, "?model=5G16-A")))
	assert.Equal(t, []string{"CLAIM7", "IMSI001010000000001"}, keys(listCpes(t, h, "?online=true")))
	assert.Equal(t, []string{"IMSI001010000000002"}, keys(listCpes(t, h, "?online=false")))
	assert.Equal(t, []string{"CLAIM7"}, keys(listCpes(t, h, "?gateway_id=g2")))
	assert.Equal(t, []string{"CLAIM7"}, keys(listCpes(t, h, "?mode=claimed")))
	assert.Equal(t, []string{"IMSI001010000000001"}, keys(listCpes(t, h, "?mode=core&online=true&gateway_id=g1")))

	rec := serve(t, h, http.MethodGet, CpesPath, "/magma/v1/acs/n1/cpes?online=yes", map[string]string{"network_id": "n1"}, "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestListCpesStaleReportIsOffline(t *testing.T) {
	setupNetwork(t)
	h := newHandlers(nil)
	reportCpe(t, "hw1", titan("IMSI001010000000001", cpestate.ModeCore, true))

	h.now = func() time.Time { return time.Now().Add(stateStaleAfter + time.Minute) }
	cpes := listCpes(t, h, "")
	require.Len(t, cpes, 1)
	assert.False(t, cpes[0].Online)
}

func TestListCpesHidesCpesSilentPastTheHorizon(t *testing.T) {
	setupNetwork(t)
	h := newHandlers(nil).WithStaleCpeAfter(24 * time.Hour)
	now := time.Now()
	fresh := titan("IMSI001010000000001", cpestate.ModeCore, true)
	fresh.LastInform = float64(now.Add(-time.Hour).Unix())
	gone := titan("IMSI001010000000002", cpestate.ModeCore, false)
	gone.LastInform = float64(now.Add(-48 * time.Hour).Unix())
	reportCpe(t, "hw1", fresh)
	reportCpe(t, "hw1", gone)

	assert.Equal(t, []string{"IMSI001010000000001"}, keys(listCpes(t, h, "")))
	assert.Equal(t, []string{"IMSI001010000000001", "IMSI001010000000002"}, keys(listCpes(t, h, "?include_stale=true")))
	assert.Equal(t, []string{"IMSI001010000000001"}, keys(listCpes(t, h, "?include_stale=false")))
	rec := serve(t, h, http.MethodGet, CpesPath, "/magma/v1/acs/n1/cpes?include_stale=1", map[string]string{"network_id": "n1"}, "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	// The detail still answers for a stale CPE.
	rec = serve(t, h, http.MethodGet, CpePath, "/magma/v1/acs/n1/cpes/IMSI001010000000002",
		map[string]string{"network_id": "n1", "cpe_key": "IMSI001010000000002"}, "")
	assert.Equal(t, http.StatusOK, rec.Code)

	// The default horizon is acsd's 7 days.
	assert.Len(t, listCpes(t, newHandlers(nil).WithStaleCpeAfter(0), ""), 2)
}

func TestGetCpe(t *testing.T) {
	setupNetwork(t)
	h := newHandlers(nil)
	reportCpe(t, "hw2", titan("IMSI001010000000001", cpestate.ModeCore, true))

	rec := serve(t, h, http.MethodGet, CpePath, "/magma/v1/acs/n1/cpes/IMSI001010000000001",
		map[string]string{"network_id": "n1", "cpe_key": "IMSI001010000000001"}, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var detail models.AcsCpeDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	assert.Equal(t, "IMSI001010000000001", detail.Cpe.CpeKey)
	assert.Equal(t, "g2", detail.Cpe.GatewayID)
	assert.Equal(t, map[string]interface{}{"identity": map[string]interface{}{"model_name": "Titan 4000"}}, detail.Model)
	assert.Equal(t, cpestate.ReachConnectionRequest, detail.Cpe.Reach)
	assert.Empty(t, detail.Cpe.ReachReason)
	require.NotNil(t, detail.Cpe.NextInform)
	assert.Equal(t, informAt.Add(300*time.Second+500*time.Millisecond).UTC(), time.Time(*detail.Cpe.NextInform))

	rec = serve(t, h, http.MethodGet, CpePath, "/magma/v1/acs/n1/cpes/IMSI9",
		map[string]string{"network_id": "n1", "cpe_key": "IMSI9"}, "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// An IMSI that moved from hw1 to hw2: hw1's acsd keeps reporting it,
// offline, with its older Inform, and must not take the row back.
func TestCpeReportedByTwoGatewaysIsServedByTheNewestInform(t *testing.T) {
	setupNetwork(t)
	h := newHandlers(nil)
	moved := titan("IMSI001010000000001", cpestate.ModeCore, true)
	old := titan("IMSI001010000000001", cpestate.ModeCore, false)
	old.LastInform = moved.LastInform - 3600
	reportCpe(t, "hw2", moved)
	reportCpe(t, "hw1", old) // reported last, as every minute

	cpes := listCpes(t, h, "")
	require.Len(t, cpes, 1)
	assert.Equal(t, "g2", cpes[0].GatewayID)
	assert.True(t, cpes[0].Online)
	assert.Empty(t, cpes[0].ConflictingGatewayIds)
	assert.Equal(t, []string{"IMSI001010000000001"}, keys(listCpes(t, h, "?gateway_id=g2")))
	assert.Empty(t, listCpes(t, h, "?gateway_id=g1"))

	rec := serve(t, h, http.MethodGet, CpePath, "/magma/v1/acs/n1/cpes/IMSI001010000000001",
		map[string]string{"network_id": "n1", "cpe_key": "IMSI001010000000001"}, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var detail models.AcsCpeDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	assert.Equal(t, "hw2", detail.Cpe.HardwareID)
}

// One claim ID on two AGWs: two CPEs online under one key.
func TestClaimIDOnTwoGatewaysIsFlagged(t *testing.T) {
	setupNetwork(t)
	h := newHandlers(nil)
	inform := float64(informAt.Unix())
	a := &cpestate.CpeView{CpeKey: "CLAIM7", Mode: cpestate.ModeClaimed, Online: true, LastInform: inform}
	b := &cpestate.CpeView{CpeKey: "CLAIM7", Mode: cpestate.ModeClaimed, Online: true, LastInform: inform - 10}
	reportCpe(t, "hw1", a)
	reportCpe(t, "hw2", b)

	cpes := listCpes(t, h, "")
	require.Len(t, cpes, 1)
	assert.Equal(t, "g1", cpes[0].GatewayID)
	assert.Equal(t, []string{"g2"}, cpes[0].ConflictingGatewayIds)

	// A stale report of the other gateway is no conflict.
	reportCpeAt(t, "hw2", b, time.Now().Add(-stateStaleAfter-time.Minute))
	assert.Empty(t, listCpes(t, h, "")[0].ConflictingGatewayIds)
}

func TestRequireFeatureGuardsEveryRoute(t *testing.T) {
	orig := requireFeature
	defer func() { requireFeature = orig }()
	requireFeature = func(echo.Context) error { return echo.NewHTTPError(http.StatusForbidden, "acs not entitled") }

	for _, hd := range NewHandlers(nil, nil).GetHandlers() {
		c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
		err := hd.HandlerFunc(c)
		httpErr, ok := err.(*echo.HTTPError)
		require.True(t, ok, hd.Path)
		assert.Equal(t, http.StatusForbidden, httpErr.Code, hd.Path)
	}
}
