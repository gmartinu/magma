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
	"net/http"
	"net/http/httptest"
	"strings"
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

const dev = "00259E-Titan4000-SN1"

func newTaskStore(t *testing.T) storage.ACSStorage {
	db, err := sqorc.Open("sqlite3", ":memory:?_foreign_keys=1")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	store := storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder())
	require.NoError(t, store.Init())
	require.NoError(t, store.UpsertDevice(&storage.Device{DeviceID: dev, NetworkID: "n1", OUI: "00259E", SerialNumber: "SN1"}))
	require.NoError(t, store.UpsertDevice(&storage.Device{DeviceID: "unclaimed", OUI: "00259E", SerialNumber: "SN9"}))
	return store
}

// post calls the create handler and decodes the task it returns.
func post(t *testing.T, h echo.HandlerFunc, network, device, body string) (int, *models.AcsTask, string) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("network_id", "device_id")
	c.SetParamValues(network, device)
	if err := h(c); err != nil {
		he := err.(*echo.HTTPError)
		return he.Code, nil, he.Message.(string)
	}
	task := &models.AcsTask{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), task))
	return rec.Code, task, ""
}

func TestCreateTask(t *testing.T) {
	clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	defer clock.UnfreezeClock(t)
	store := newTaskStore(t)
	h := handlers.NewHandlers(store)
	h.TaskMaxAttempts, h.TaskTTL = 2, time.Hour
	create := tests.GetHandlerByPathAndMethod(t, h.GetHandlers(), handlers.TasksPath, obsidian.POST).HandlerFunc

	status, task, _ := post(t, create, "n1", dev, `{"type":"reboot"}`)
	require.Equal(t, http.StatusCreated, status)
	assert.Len(t, task.ID, 32)
	assert.Equal(t, &models.AcsTask{
		ID: task.ID, DeviceID: dev, Type: "reboot", Status: "pending", MaxAttempts: 2,
		Created:  strfmt.DateTime(time.Unix(1000, 0).UTC()),
		Updated:  strfmt.DateTime(time.Unix(1000, 0).UTC()),
		Deadline: strfmt.DateTime(time.Unix(4600, 0).UTC()),
	}, task)
	stored, err := store.GetTask(task.ID)
	require.NoError(t, err)
	assert.Equal(t, "n1", stored.NetworkID)
	assert.Equal(t, int64(4600), stored.DeadlineSec)

	status, task, _ = post(t, create, "n1", dev, `{"type":"set_parameter_values","max_attempts":5,"ttl_sec":60,
		"parameter_values":[{"name":"Device.ManagementServer.PeriodicInformInterval","value":"300","type":"xsd:unsignedInt"}]}`)
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, int64(5), task.MaxAttempts)
	assert.Equal(t, strfmt.DateTime(time.Unix(1060, 0).UTC()), task.Deadline)
	assert.Equal(t, []models.AcsParameterValue{{Name: "Device.ManagementServer.PeriodicInformInterval", Value: "300", Type: "xsd:unsignedInt"}}, task.ParameterValues)

	for _, tc := range []struct {
		network, device, body string
		status                int
		msg                   string
	}{
		{"n1", dev, `{"type":"upgrade"}`, http.StatusBadRequest, "type"},
		{"n1", dev, `{"type":"get_parameter_values"}`, http.StatusBadRequest, "parameter_names"},
		{"n1", dev, `{"type":"reboot","parameter_names":["x"]}`, http.StatusBadRequest, "no parameters"},
		{"n1", dev, `{"type":"reboot","max_attempts":-1}`, http.StatusBadRequest, "max_attempts"},
		{"n1", dev, `{`, http.StatusBadRequest, ""},
		{"n2", dev, `{"type":"reboot"}`, http.StatusNotFound, "device not found"},
		{"n1", "unclaimed", `{"type":"reboot"}`, http.StatusNotFound, "device not found"},
		{"n1", "missing", `{"type":"reboot"}`, http.StatusNotFound, "device not found"},
		{"n1", "", `{"type":"reboot"}`, http.StatusBadRequest, "missing device ID"},
	} {
		status, _, msg := post(t, create, tc.network, tc.device, tc.body)
		assert.Equal(t, tc.status, status, tc.body)
		assert.Contains(t, msg, tc.msg, tc.body)
	}
}

func TestListAndGetTasks(t *testing.T) {
	store := newTaskStore(t)
	h := handlers.NewHandlers(store)
	e := echo.New()
	list := tests.GetHandlerByPathAndMethod(t, h.GetHandlers(), handlers.TasksPath, obsidian.GET).HandlerFunc
	get := tests.GetHandlerByPathAndMethod(t, h.GetHandlers(), handlers.TaskPath, obsidian.GET).HandlerFunc

	tc := tests.Test{
		Method: "GET", URL: "/magma/v1/acs/n1/devices/" + dev + "/tasks", Handler: list,
		ParamNames: []string{"network_id", "device_id"}, ParamValues: []string{"n1", dev},
		ExpectedStatus: http.StatusOK, ExpectedResult: tests.JSONMarshaler([]*models.AcsTask{}),
	}
	tests.RunUnitTest(t, e, tc)

	require.NoError(t, store.CreateTask(&storage.Task{
		TaskID: "t1", DeviceID: dev, NetworkID: "n1", Type: "get_parameter_values", Args: `{"parameter_names":["Device.DeviceInfo."]}`,
		Status: storage.TaskPending, MaxAttempts: 3, CreatedSec: 10, UpdatedSec: 10,
	}))
	require.NoError(t, store.CompleteTask("t1", `{"values":{"Device.DeviceInfo.UpTime":"5"}}`))
	done, _ := store.GetTask("t1")
	expected := &models.AcsTask{
		ID: "t1", DeviceID: dev, Type: "get_parameter_values", Status: "done", MaxAttempts: 3,
		ParameterNames: []string{"Device.DeviceInfo."},
		Result:         map[string]interface{}{"values": map[string]interface{}{"Device.DeviceInfo.UpTime": "5"}},
		Created:        strfmt.DateTime(time.Unix(10, 0).UTC()),
		Updated:        strfmt.DateTime(time.Unix(done.UpdatedSec, 0).UTC()),
	}
	tc.ExpectedResult = tests.JSONMarshaler([]*models.AcsTask{expected})
	tests.RunUnitTest(t, e, tc)

	tc = tests.Test{
		Method: "GET", URL: "/magma/v1/acs/n1/devices/" + dev + "/tasks/t1", Handler: get,
		ParamNames: []string{"network_id", "device_id", "task_id"}, ParamValues: []string{"n1", dev, "t1"},
		ExpectedStatus: http.StatusOK, ExpectedResult: expected,
	}
	tests.RunUnitTest(t, e, tc)

	tc.ParamValues = []string{"n1", dev, "t2"}
	tc.ExpectedStatus, tc.ExpectedResult, tc.ExpectedError = http.StatusNotFound, nil, "task not found"
	tests.RunUnitTest(t, e, tc)
	tc.ParamValues = []string{"n2", dev, "t1"}
	tc.ExpectedError = "device not found"
	tests.RunUnitTest(t, e, tc)
}
