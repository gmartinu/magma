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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/labstack/echo/v4"

	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/tasks"
	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/services/obsidian"
)

// device returns the device of the path, which must be claimed by the network.
func (h *Handlers) device(c echo.Context) (*storage.Device, error) {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nil, nerr
	}
	deviceID := c.Param("device_id")
	if deviceID == "" {
		return nil, echo.NewHTTPError(http.StatusBadRequest, "missing device ID")
	}
	d, err := h.store.GetDevice(deviceID)
	if err != nil {
		return nil, echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	// A device of another network, or of none, is reported as missing.
	if d == nil || d.NetworkID != networkID {
		return nil, echo.NewHTTPError(http.StatusNotFound, "device not found")
	}
	return d, nil
}

func (h *Handlers) listTasks(c echo.Context) error {
	d, err := h.device(c)
	if err != nil {
		return err
	}
	list, err := h.store.ListTasks(d.DeviceID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]*models.AcsTask, 0, len(list))
	for _, t := range list {
		m, err := (&models.AcsTask{}).FromStorage(t)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		out = append(out, m)
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handlers) getTask(c echo.Context) error {
	d, err := h.device(c)
	if err != nil {
		return err
	}
	t, err := h.store.GetTask(c.Param("task_id"))
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if t == nil || t.DeviceID != d.DeviceID {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	m, err := (&models.AcsTask{}).FromStorage(t)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, m)
}

func (h *Handlers) createTask(c echo.Context) error {
	d, err := h.device(c)
	if err != nil {
		return err
	}
	req := &models.AcsTaskRequest{}
	if err := c.Bind(req); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if err := req.Validate(strfmt.Default); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	args := req.ToArgs()
	if err := tasks.Validate(req.Type, args); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	b, err := json.Marshal(args)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}

	maxAttempts, ttl := h.TaskMaxAttempts, h.TaskTTL
	if req.MaxAttempts != nil && *req.MaxAttempts > 0 {
		maxAttempts = int(*req.MaxAttempts)
	}
	if req.TTLSec != nil && *req.TTLSec > 0 {
		ttl = time.Duration(*req.TTLSec) * time.Second
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	now := clock.Now()
	t := &storage.Task{
		TaskID:      hex.EncodeToString(id),
		DeviceID:    d.DeviceID,
		NetworkID:   d.NetworkID,
		Type:        req.Type,
		Args:        string(b),
		Status:      storage.TaskPending,
		MaxAttempts: maxAttempts,
		CreatedSec:  now.Unix(),
		UpdatedSec:  now.Unix(),
		DeadlineSec: now.Add(ttl).Unix(),
	}
	if err := h.store.CreateTask(t); err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	m, err := (&models.AcsTask{}).FromStorage(t)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusCreated, m)
}
