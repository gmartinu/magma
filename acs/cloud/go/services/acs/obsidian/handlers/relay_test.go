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
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"

	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/acs/cloud/go/services/acs/obsidian/models"
	lte_protos "magma/lte/cloud/go/protos"
)

// fakeAcsd stands in for acsd on the far side of SyncRPC.
type fakeAcsd struct {
	dialedHwIDs []string
	dialErr     error
	closed      int

	enqueued *lte_protos.EnqueueTaskRequest
	cpe      *lte_protos.Cpe
	cpeReq   *lte_protos.GetCpeRequest
	task     *lte_protos.CpeTask
	err      error
}

func (f *fakeAcsd) Dial(hwID string) (lte_protos.CpeManagerClient, context.Context, func(), error) {
	f.dialedHwIDs = append(f.dialedHwIDs, hwID)
	if f.dialErr != nil {
		return nil, nil, nil, f.dialErr
	}
	return f, context.Background(), func() { f.closed++ }, nil
}

func (f *fakeAcsd) EnqueueTask(_ context.Context, in *lte_protos.EnqueueTaskRequest, _ ...grpc.CallOption) (*lte_protos.CpeTask, error) {
	f.enqueued = in
	return f.task, f.err
}

func (f *fakeAcsd) GetTask(_ context.Context, in *lte_protos.GetTaskRequest, _ ...grpc.CallOption) (*lte_protos.CpeTask, error) {
	return f.task, f.err
}

func (f *fakeAcsd) ListCpes(context.Context, *lte_protos.ListCpesRequest, ...grpc.CallOption) (*lte_protos.ListCpesResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unused")
}

func (f *fakeAcsd) GetCpe(_ context.Context, in *lte_protos.GetCpeRequest, _ ...grpc.CallOption) (*lte_protos.Cpe, error) {
	f.cpeReq = in
	return f.cpe, f.err
}

func (f *fakeAcsd) ConnectionRequest(context.Context, *lte_protos.ConnectionRequestRequest, ...grpc.CallOption) (*lte_protos.ConnectionRequestResponse, error) {
	return nil, status.Error(codes.Unimplemented, "unused")
}

const cpeKey = "IMSI001010000000001"

func cpeParams(extra ...string) map[string]string {
	p := map[string]string{"network_id": "n1", "cpe_key": cpeKey}
	for i := 0; i+1 < len(extra); i += 2 {
		p[extra[i]] = extra[i+1]
	}
	return p
}

func rebootTask() *lte_protos.CpeTask {
	args, _ := structpb.NewStruct(map[string]interface{}{"max_attempts": 3})
	return &lte_protos.CpeTask{
		TaskId: "t1", CpeKey: cpeKey, Type: lte_protos.CpeTaskType_CPE_TASK_TYPE_REBOOT,
		Status: lte_protos.CpeTaskStatus_CPE_TASK_STATUS_PENDING, Args: args, MaxAttempts: 3,
		Created: 1700000000, Updated: 1700000001.5, Imsi: cpeKey,
	}
}

func setupRelay(t *testing.T) (*Handlers, *fakeAcsd) {
	setupNetwork(t)
	reportCpe(t, "hw2", titan(cpeKey, cpestate.ModeCore, true))
	acsd := &fakeAcsd{}
	return NewHandlers(acsd), acsd
}

func TestCreateTaskRelaysToReportingGateway(t *testing.T) {
	h, acsd := setupRelay(t)
	acsd.task = rebootTask()

	rec := serve(t, h, http.MethodPost, TasksPath, "/", cpeParams(), `{"type": "reboot", "max_attempts": 3}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, []string{"hw2"}, acsd.dialedHwIDs)
	assert.Equal(t, 1, acsd.closed)
	assert.Equal(t, cpeKey, acsd.enqueued.CpeKey)
	assert.Equal(t, lte_protos.CpeTaskType_CPE_TASK_TYPE_REBOOT, acsd.enqueued.Type)
	assert.Equal(t, int32(3), acsd.enqueued.MaxAttempts)

	var task models.AcsTask
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &task))
	assert.Equal(t, "t1", task.ID)
	assert.Equal(t, "reboot", task.Type)
	assert.Equal(t, "pending", task.Status)
	assert.Equal(t, map[string]interface{}{"max_attempts": 3.0}, task.Args)
	assert.Equal(t, time.UnixMilli(1700000001500).UTC(), time.Time(task.Updated))
	assert.Nil(t, task.Deadline)
}

func TestCreateTaskSetParameterValues(t *testing.T) {
	h, acsd := setupRelay(t)
	acsd.task = rebootTask()

	body := `{"type": "set_parameter_values", "ttl_sec": -1,
		"parameter_values": [{"name": "Device.ManagementServer.PeriodicInformInterval", "value": "300", "type": "xsd:unsignedInt"}]}`
	rec := serve(t, h, http.MethodPost, TasksPath, "/", cpeParams(), body)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, lte_protos.CpeTaskType_CPE_TASK_TYPE_SET_PARAMETER_VALUES, acsd.enqueued.Type)
	assert.Equal(t, int64(-1), acsd.enqueued.TtlSec)
	require.Len(t, acsd.enqueued.ParameterValues, 1)
	assert.Equal(t, "xsd:unsignedInt", acsd.enqueued.ParameterValues[0].Type)
}

func TestCreateTaskErrors(t *testing.T) {
	h, acsd := setupRelay(t)

	rec := serve(t, h, http.MethodPost, TasksPath, "/", cpeParams(), `{"type": "format_disk"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, acsd.dialedHwIDs, "an invalid task never reaches the gateway")

	rec = serve(t, h, http.MethodPost, TasksPath, "/", cpeParams("cpe_key", "IMSI9"), `{"type": "reboot"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "no gateway reports the CPE")

	acsd.err = status.Error(codes.InvalidArgument, "parameter_values required")
	rec = serve(t, h, http.MethodPost, TasksPath, "/", cpeParams(), `{"type": "set_parameter_values"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	acsd.err = status.Error(codes.Unavailable, "gateway gone")
	rec = serve(t, h, http.MethodPost, TasksPath, "/", cpeParams(), `{"type": "reboot"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	acsd.dialErr = errors.New("no SyncRPC stream for hw2")
	rec = serve(t, h, http.MethodPost, TasksPath, "/", cpeParams(), `{"type": "reboot"}`)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestGetTask(t *testing.T) {
	h, acsd := setupRelay(t)
	acsd.task = rebootTask()

	rec := serve(t, h, http.MethodGet, TaskPath, "/", cpeParams("task_id", "t1"), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var task models.AcsTask
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &task))
	assert.Equal(t, "t1", task.ID)

	acsd.task.CpeKey = "IMSI001010000000002"
	rec = serve(t, h, http.MethodGet, TaskPath, "/", cpeParams("task_id", "t1"), "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "a task of another CPE")

	acsd.err = status.Error(codes.NotFound, "unknown task")
	rec = serve(t, h, http.MethodGet, TaskPath, "/", cpeParams("task_id", "t9"), "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestListTasks(t *testing.T) {
	h, acsd := setupRelay(t)
	done := rebootTask()
	done.TaskId, done.Status, done.Deadline = "t0", lte_protos.CpeTaskStatus_CPE_TASK_STATUS_DONE, 1700003600
	acsd.cpe = &lte_protos.Cpe{CpeKey: cpeKey, Tasks: []*lte_protos.CpeTask{done, rebootTask()}}

	rec := serve(t, h, http.MethodGet, TasksPath, "/", cpeParams(), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, acsd.cpeReq.IncludeParameters)
	var tasks []models.AcsTask
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &tasks))
	require.Len(t, tasks, 2)
	assert.Equal(t, "t0", tasks[0].ID)
	assert.Equal(t, "done", tasks[0].Status)
	require.NotNil(t, tasks[0].Deadline)
	assert.Equal(t, "t1", tasks[1].ID)
}

func TestListParameters(t *testing.T) {
	h, acsd := setupRelay(t)
	acsd.cpe = &lte_protos.Cpe{CpeKey: cpeKey, Parameters: map[string]string{
		"Device.Cellular.Interface.1.RSRP": "-95",
		"Device.Cellular.Interface.1.RSRQ": "-11",
		"Device.DeviceInfo.SerialNumber":   "SN1",
	}}

	rec := serve(t, h, http.MethodGet, ParametersPath, "/?prefix=Device.Cellular.", cpeParams(), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, acsd.cpeReq.IncludeParameters)
	assert.JSONEq(t, `[
		{"name": "Device.Cellular.Interface.1.RSRP", "value": "-95"},
		{"name": "Device.Cellular.Interface.1.RSRQ", "value": "-11"}
	]`, rec.Body.String())

	acsd.err = status.Error(codes.NotFound, "acsd lost the CPE")
	rec = serve(t, h, http.MethodGet, ParametersPath, "/", cpeParams(), "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
