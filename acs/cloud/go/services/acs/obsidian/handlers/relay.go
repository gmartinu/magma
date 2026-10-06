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
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"magma/acs/cloud/go/services/acs/obsidian/models"
	lte_protos "magma/lte/cloud/go/protos"
	"magma/orc8r/cloud/go/services/dispatcher/gateway_registry"
	"magma/orc8r/cloud/go/services/obsidian"
)

const (
	TasksPath      = CpePath + obsidian.UrlSep + "tasks"
	TaskPath       = TasksPath + obsidian.UrlSep + ":task_id"
	ParametersPath = CpePath + obsidian.UrlSep + "parameters"

	relayTimeout = 20 * time.Second

	taskTypePrefix   = "CPE_TASK_TYPE_"
	taskStatusPrefix = "CPE_TASK_STATUS_"
)

// CpeManagers reaches acsd's CpeManager on a gateway. The returned close
// releases the connection, which is per call.
type CpeManagers interface {
	Dial(hwID string) (lte_protos.CpeManagerClient, context.Context, func(), error)
}

type syncRPCCpeManagers struct{}

// NewSyncRPCCpeManagers relays over the gateway's SyncRPC stream, as
// ctraced does.
func NewSyncRPCCpeManagers() CpeManagers {
	return syncRPCCpeManagers{}
}

func (syncRPCCpeManagers) Dial(hwID string) (lte_protos.CpeManagerClient, context.Context, func(), error) {
	conn, ctx, err := gateway_registry.GetGatewayConnection(gateway_registry.GwAcsd, hwID)
	if err != nil {
		return nil, nil, nil, err
	}
	return lte_protos.NewCpeManagerClient(conn), ctx, func() { _ = conn.Close() }, nil
}

func (h *Handlers) relayHandlers() []obsidian.Handler {
	return []obsidian.Handler{
		{Path: ParametersPath, Methods: obsidian.GET, HandlerFunc: h.listParameters},
		{Path: TasksPath, Methods: obsidian.GET, HandlerFunc: h.listTasks},
		{Path: TasksPath, Methods: obsidian.POST, HandlerFunc: h.createTask},
		{Path: TaskPath, Methods: obsidian.GET, HandlerFunc: h.getTask},
	}
}

// relay calls acsd on the gateway serving the CPE.
func (h *Handlers) relay(c echo.Context, call func(context.Context, lte_protos.CpeManagerClient, string) error) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	cpeKey := c.Param("cpe_key")
	owner, _, err := h.loadCpe(networkID, cpeKey)
	if err != nil {
		return err
	}
	client, gwCtx, closeConn, err := h.cpes.Dial(owner.HardwareID)
	if err != nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "gateway "+owner.HardwareID+" unreachable: "+err.Error())
	}
	defer closeConn()
	ctx, cancel := context.WithTimeout(gwCtx, relayTimeout)
	defer cancel()
	if err := call(ctx, client, cpeKey); err != nil {
		if _, ok := err.(*echo.HTTPError); ok {
			return err
		}
		return relayError(err)
	}
	return nil
}

func relayError(err error) *echo.HTTPError {
	code := http.StatusInternalServerError
	switch status.Code(err) {
	case codes.InvalidArgument:
		code = http.StatusBadRequest
	case codes.NotFound:
		code = http.StatusNotFound
	case codes.Unimplemented:
		code = http.StatusNotImplemented
	case codes.Unavailable, codes.DeadlineExceeded:
		code = http.StatusServiceUnavailable
	}
	return echo.NewHTTPError(code, "acsd: "+status.Convert(err).Message())
}

func (h *Handlers) listParameters(c echo.Context) error {
	prefix := c.QueryParam("prefix")
	return h.relay(c, func(ctx context.Context, client lte_protos.CpeManagerClient, cpeKey string) error {
		cpe, err := client.GetCpe(ctx, &lte_protos.GetCpeRequest{CpeKey: cpeKey, IncludeParameters: true})
		if err != nil {
			return err
		}
		out := []*models.AcsParameter{}
		for name, value := range cpe.Parameters {
			if strings.HasPrefix(name, prefix) {
				out = append(out, &models.AcsParameter{Name: name, Value: value})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
		return c.JSON(http.StatusOK, out)
	})
}

func (h *Handlers) listTasks(c echo.Context) error {
	return h.relay(c, func(ctx context.Context, client lte_protos.CpeManagerClient, cpeKey string) error {
		cpe, err := client.GetCpe(ctx, &lte_protos.GetCpeRequest{CpeKey: cpeKey})
		if err != nil {
			return err
		}
		out := make([]*models.AcsTask, 0, len(cpe.Tasks))
		for _, t := range cpe.Tasks {
			out = append(out, toTask(t))
		}
		return c.JSON(http.StatusOK, out)
	})
}

func (h *Handlers) createTask(c echo.Context) error {
	req := &models.AcsTaskRequest{}
	if err := c.Bind(req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := req.Validate(strfmt.Default); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	enqueue := toEnqueueRequest(req)
	return h.relay(c, func(ctx context.Context, client lte_protos.CpeManagerClient, cpeKey string) error {
		enqueue.CpeKey = cpeKey
		task, err := client.EnqueueTask(ctx, enqueue)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, toTask(task))
	})
}

func (h *Handlers) getTask(c echo.Context) error {
	taskID := c.Param("task_id")
	return h.relay(c, func(ctx context.Context, client lte_protos.CpeManagerClient, cpeKey string) error {
		task, err := client.GetTask(ctx, &lte_protos.GetTaskRequest{TaskId: taskID})
		if err != nil {
			return err
		}
		// acsd's task IDs are per gateway, not per CPE; do not leak another CPE's task.
		if task.CpeKey != cpeKey {
			return echo.NewHTTPError(http.StatusNotFound, "task "+taskID+" is not a task of "+cpeKey)
		}
		return c.JSON(http.StatusOK, toTask(task))
	})
}

func toEnqueueRequest(req *models.AcsTaskRequest) *lte_protos.EnqueueTaskRequest {
	out := &lte_protos.EnqueueTaskRequest{
		Type:           lte_protos.CpeTaskType(lte_protos.CpeTaskType_value[taskTypePrefix+strings.ToUpper(req.Type)]),
		ParameterNames: req.ParameterNames,
		ParameterPath:  req.ParameterPath,
		NextLevel:      req.NextLevel,
		TtlSec:         req.TTLSec,
	}
	if req.MaxAttempts != nil {
		out.MaxAttempts = *req.MaxAttempts
	}
	for _, v := range req.ParameterValues {
		out.ParameterValues = append(out.ParameterValues, &lte_protos.CpeParameterValue{Name: v.Name, Value: v.Value, Type: v.Type})
	}
	return out
}

func toTask(t *lte_protos.CpeTask) *models.AcsTask {
	out := &models.AcsTask{
		ID:          t.TaskId,
		CpeKey:      t.CpeKey,
		Type:        strings.ToLower(strings.TrimPrefix(t.Type.String(), taskTypePrefix)),
		Status:      strings.ToLower(strings.TrimPrefix(t.Status.String(), taskStatusPrefix)),
		Attempts:    t.Attempts,
		MaxAttempts: t.MaxAttempts,
		FaultCode:   t.FaultCode,
		FaultString: t.FaultString,
		Created:     strfmt.DateTime(time.UnixMilli(int64(t.Created * 1000)).UTC()),
		Updated:     strfmt.DateTime(time.UnixMilli(int64(t.Updated * 1000)).UTC()),
		Deadline:    unixTime(t.Deadline),
		Imsi:        t.Imsi,
	}
	if t.Args != nil {
		out.Args = t.Args.AsMap()
	}
	if t.Result != nil {
		out.Result = t.Result.AsMap()
	}
	return out
}
