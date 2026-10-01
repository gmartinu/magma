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

package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/obsidian/tests"
	"magma/orc8r/cloud/go/sqorc"
)

func newStore(t *testing.T) storage.ACSStorage {
	db, err := sqorc.Open("sqlite3", ":memory:?_foreign_keys=1")
	require.NoError(t, err)
	store := storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder())
	require.NoError(t, store.Init())
	return store
}

// call runs a handler with path parameters and a query string and returns
// the status and body, or the error message.
func call(t *testing.T, h echo.HandlerFunc, method, query string, params ...string) (int, string) {
	req := httptest.NewRequest(method, "/?"+query, nil)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	var names, values []string
	for i := 0; i+1 < len(params); i += 2 {
		names, values = append(names, params[i]), append(values, params[i+1])
	}
	c.SetParamNames(names...)
	c.SetParamValues(values...)
	if err := h(c); err != nil {
		he := err.(*echo.HTTPError)
		return he.Code, he.Message.(string)
	}
	return rec.Code, rec.Body.String()
}

func handler(t *testing.T, h *handlers.Handlers, path string, method obsidian.HttpMethod) echo.HandlerFunc {
	return tests.GetHandlerByPathAndMethod(t, h.GetHandlers(), path, method).HandlerFunc
}

func decodeDevices(t *testing.T, body string) []string {
	var out []*models.AcsDevice
	require.NoError(t, json.Unmarshal([]byte(body), &out))
	ids := []string{}
	for _, d := range out {
		ids = append(ids, d.DeviceID)
	}
	return ids
}

// seen records devices the way an Inform does: without a network.
func seen(t *testing.T, store storage.ACSStorage, devices ...*storage.Device) {
	for _, d := range devices {
		require.NoError(t, store.UpsertDevice(d))
	}
}

func TestListDevices(t *testing.T) {
	store := newStore(t)
	h := handlers.NewHandlers(store)
	listDevices := handler(t, h, handlers.DevicesPath, obsidian.GET)

	tc := tests.Test{
		Method:         "GET",
		URL:            "/magma/v1/acs/n1/devices",
		Handler:        listDevices,
		ParamNames:     []string{"network_id"},
		ParamValues:    []string{"n1"},
		ExpectedStatus: http.StatusOK,
		ExpectedResult: tests.JSONMarshaler([]*models.AcsDevice{}),
	}
	tests.RunUnitTest(t, echo.New(), tc)

	clock.SetAndFreezeClock(t, time.Unix(250, 0))
	defer clock.UnfreezeClock(t)
	seen(t, store, &storage.Device{
		DeviceID: "00259E-Titan4000-SN1", OUI: "00259E", ProductClass: "Titan4000", SerialNumber: "SN1",
		Firmware: "1.0.0", FirstSeenSec: 100, LastSeenSec: 200, InformIntervalSec: 300,
	})
	require.NoError(t, store.ClaimDevice("00259E-Titan4000-SN1", "n1"))
	seen(t, store, &storage.Device{DeviceID: "00259E-Titan4000-SN2", OUI: "00259E", SerialNumber: "SN2", FirstSeenSec: 100, LastSeenSec: 100})
	require.NoError(t, store.ClaimDevice("00259E-Titan4000-SN2", "n2"))
	tc.ExpectedResult = tests.JSONMarshaler([]*models.AcsDevice{{
		DeviceID:               "00259E-Titan4000-SN1",
		Oui:                    "00259E",
		ProductClass:           "Titan4000",
		SerialNumber:           "SN1",
		Firmware:               "1.0.0",
		FirstSeen:              strfmt.DateTime(time.Unix(100, 0).UTC()),
		LastSeen:               strfmt.DateTime(time.Unix(200, 0).UTC()),
		Online:                 true,
		PeriodicInformInterval: 300,
	}})
	tests.RunUnitTest(t, echo.New(), tc)

	tc.ParamNames, tc.ParamValues = nil, nil
	tc.ExpectedStatus = http.StatusBadRequest
	tc.ExpectedResult = nil
	tc.ExpectedError = "Missing Network ID"
	tests.RunUnitTest(t, echo.New(), tc)

	status, msg := call(t, listDevices, http.MethodGet, "online=maybe", "network_id", "n1")
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Equal(t, "online must be true or false", msg)

	status, msg = call(t, handler(t, handlers.NewHandlers(failingStore{store}), handlers.DevicesPath, obsidian.GET), http.MethodGet, "", "network_id", "n1")
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, "db down", msg)
}

func TestListDevices_Filters(t *testing.T) {
	store := newStore(t)
	h := handlers.NewHandlers(store)
	h.Online = storage.OnlinePolicy{IntervalMultiple: 2, DefaultIntervalSec: 600}
	listDevices := handler(t, h, handlers.DevicesPath, obsidian.GET)
	listUnclaimed := handler(t, h, handlers.UnclaimedPath, obsidian.GET)

	clock.SetAndFreezeClock(t, time.Unix(10000, 0))
	defer clock.UnfreezeClock(t)
	seen(t, store,
		// Interval 300: online while its last Inform is at most 600s old.
		&storage.Device{DeviceID: "A-Titan4000-1", OUI: "A", SerialNumber: "1", Model: "Titan 4000", LastSeenSec: 9400, InformIntervalSec: 300},
		&storage.Device{DeviceID: "A-Titan4000-2", OUI: "A", SerialNumber: "2", Model: "Titan 4000", LastSeenSec: 9399, InformIntervalSec: 300},
		// Unknown interval: the default 600 applies, online for 1200s.
		&storage.Device{DeviceID: "A-Titan5400-3", OUI: "A", SerialNumber: "3", Model: "Titan 5400", LastSeenSec: 8800},
		&storage.Device{DeviceID: "A-Titan5400-4", OUI: "A", SerialNumber: "4", Model: "Titan 5400", LastSeenSec: 8000},
	)
	for _, id := range []string{"A-Titan4000-1", "A-Titan4000-2", "A-Titan5400-3"} {
		require.NoError(t, store.ClaimDevice(id, "n1"))
	}

	for query, expected := range map[string][]string{
		"":                             {"A-Titan4000-1", "A-Titan4000-2", "A-Titan5400-3"},
		"model=Titan+4000":             {"A-Titan4000-1", "A-Titan4000-2"},
		"online=true":                  {"A-Titan4000-1", "A-Titan5400-3"},
		"online=false":                 {"A-Titan4000-2"},
		"model=Titan+4000&online=true": {"A-Titan4000-1"},
		"model=Titan+9":                {},
	} {
		status, body := call(t, listDevices, http.MethodGet, query, "network_id", "n1")
		require.Equal(t, http.StatusOK, status, query)
		assert.Equal(t, expected, decodeDevices(t, body), query)
	}

	status, body := call(t, listUnclaimed, http.MethodGet, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"A-Titan5400-4"}, decodeDevices(t, body))
	status, body = call(t, listUnclaimed, http.MethodGet, "online=true")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{}, decodeDevices(t, body))
}

func TestClaimDevice(t *testing.T) {
	clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	defer clock.UnfreezeClock(t)
	store := newStore(t)
	h := handlers.NewHandlers(store)
	claim := handler(t, h, handlers.ClaimPath, obsidian.POST)
	listUnclaimed := handler(t, h, handlers.UnclaimedPath, obsidian.GET)
	listDevices := handler(t, h, handlers.DevicesPath, obsidian.GET)
	seen(t, store,
		&storage.Device{DeviceID: "A-P-1", OUI: "A", ProductClass: "P", SerialNumber: "1", FirstSeenSec: 900, LastSeenSec: 990},
		&storage.Device{DeviceID: "A-P-2", OUI: "A", ProductClass: "P", SerialNumber: "2", FirstSeenSec: 900, LastSeenSec: 990},
	)

	status, body := call(t, listUnclaimed, http.MethodGet, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"A-P-1", "A-P-2"}, decodeDevices(t, body))

	status, body = call(t, claim, http.MethodPost, "", "network_id", "n1", "device_id", "A-P-1")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{
		"device_id": "A-P-1", "oui": "A", "product_class": "P", "serial_number": "1",
		"first_seen": "1970-01-01T00:15:00.000Z", "last_seen": "1970-01-01T00:16:30.000Z", "online": true
	}`, body)

	status, body = call(t, listUnclaimed, http.MethodGet, "")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"A-P-2"}, decodeDevices(t, body))
	status, body = call(t, listDevices, http.MethodGet, "", "network_id", "n1")
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, []string{"A-P-1"}, decodeDevices(t, body))

	// A claimed device cannot be taken over, by another network or again.
	for _, network := range []string{"n2", "n1"} {
		status, msg := call(t, claim, http.MethodPost, "", "network_id", network, "device_id", "A-P-1")
		assert.Equal(t, http.StatusConflict, status)
		assert.Equal(t, "device already claimed", msg)
	}
	status, msg := call(t, claim, http.MethodPost, "", "network_id", "n1", "device_id", "A-P-9")
	assert.Equal(t, http.StatusNotFound, status)
	assert.Equal(t, "device not found", msg)
	status, _ = call(t, claim, http.MethodPost, "", "network_id", "n1")
	assert.Equal(t, http.StatusBadRequest, status)
}

func TestGetDevice(t *testing.T) {
	clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	defer clock.UnfreezeClock(t)
	store := newStore(t)
	h := handlers.NewHandlers(store)
	getDevice := handler(t, h, handlers.DevicePath, obsidian.GET)
	seen(t, store, &storage.Device{
		DeviceID: "A-P-1", OUI: "A", ProductClass: "P", SerialNumber: "1", FirstSeenSec: 900, LastSeenSec: 990,
		LastInformEvents: []string{"1 BOOT", "4 VALUE CHANGE"},
	})

	// Unclaimed devices can only be listed.
	status, _ := call(t, getDevice, http.MethodGet, "", "network_id", "n1", "device_id", "A-P-1")
	assert.Equal(t, http.StatusNotFound, status)

	require.NoError(t, store.ClaimDevice("A-P-1", "n1"))
	status, body := call(t, getDevice, http.MethodGet, "", "network_id", "n1", "device_id", "A-P-1")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{
		"device": {
			"device_id": "A-P-1", "oui": "A", "product_class": "P", "serial_number": "1",
			"first_seen": "1970-01-01T00:15:00.000Z", "last_seen": "1970-01-01T00:16:30.000Z", "online": true
		},
		"last_inform": {"time": "1970-01-01T00:16:30.000Z", "events": ["1 BOOT", "4 VALUE CHANGE"]}
	}`, body)

	require.NoError(t, store.PutDeviceState(&storage.DeviceState{
		DeviceID: "A-P-1", Handler: "generic", Model: `{"cellular":{"rsrp":-95}}`, UpdatedSec: 995,
	}))
	status, body = call(t, getDevice, http.MethodGet, "", "network_id", "n1", "device_id", "A-P-1")
	require.Equal(t, http.StatusOK, status)
	detail := map[string]interface{}{}
	require.NoError(t, json.Unmarshal([]byte(body), &detail))
	assert.Equal(t, map[string]interface{}{"cellular": map[string]interface{}{"rsrp": -95.0}}, detail["state"])
	assert.Equal(t, "1970-01-01T00:16:35.000Z", detail["state_updated"])

	status, _ = call(t, getDevice, http.MethodGet, "", "network_id", "n2", "device_id", "A-P-1")
	assert.Equal(t, http.StatusNotFound, status)
}

func TestListParameters(t *testing.T) {
	store := newStore(t)
	h := handlers.NewHandlers(store)
	listParameters := handler(t, h, handlers.ParametersPath, obsidian.GET)
	seen(t, store, &storage.Device{DeviceID: "A-P-1", OUI: "A", SerialNumber: "1"})
	require.NoError(t, store.ClaimDevice("A-P-1", "n1"))

	status, body := call(t, listParameters, http.MethodGet, "", "network_id", "n1", "device_id", "A-P-1")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, body)

	require.NoError(t, store.MergeParameters("A-P-1", map[string]string{
		"Device.DeviceInfo.SoftwareVersion": "1.0", "Device.Cellular.Interface.1.RSRP": "-95",
	}, 100))
	require.NoError(t, store.MergeParameters("A-P-1", map[string]string{"Device.Cellular.Interface.1.RSRP": "-90"}, 200))

	status, body = call(t, listParameters, http.MethodGet, "", "network_id", "n1", "device_id", "A-P-1")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[
		{"name": "Device.Cellular.Interface.1.RSRP", "value": "-90", "updated": "1970-01-01T00:03:20.000Z"},
		{"name": "Device.DeviceInfo.SoftwareVersion", "value": "1.0", "updated": "1970-01-01T00:01:40.000Z"}
	]`, body)

	status, body = call(t, listParameters, http.MethodGet, "prefix=Device.DeviceInfo.", "network_id", "n1", "device_id", "A-P-1")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[{"name": "Device.DeviceInfo.SoftwareVersion", "value": "1.0", "updated": "1970-01-01T00:01:40.000Z"}]`, body)

	status, _ = call(t, listParameters, http.MethodGet, "", "network_id", "n2", "device_id", "A-P-1")
	assert.Equal(t, http.StatusNotFound, status)
}

type failingStore struct {
	storage.ACSStorage
}

func (failingStore) FindDevices(storage.DeviceFilter) ([]*storage.Device, error) {
	return nil, errors.New("db down")
}
