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
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"magma/acs/cloud/go/acs"
	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/services/obsidian"
)

const (
	ManageNetworkPath = obsidian.V1Root + acs.ModuleName + obsidian.UrlSep + ":network_id"
	DevicesPath       = ManageNetworkPath + obsidian.UrlSep + "devices"
	TasksPath         = DevicesPath + obsidian.UrlSep + ":device_id" + obsidian.UrlSep + "tasks"
	TaskPath          = TasksPath + obsidian.UrlSep + ":task_id"
)

type Handlers struct {
	store storage.ACSStorage
	// TaskMaxAttempts and TaskTTL apply to tasks created without them.
	TaskMaxAttempts int
	TaskTTL         time.Duration
}

func NewHandlers(store storage.ACSStorage) *Handlers {
	return &Handlers{store: store, TaskMaxAttempts: 3, TaskTTL: 7 * 24 * time.Hour}
}

func (h *Handlers) GetHandlers() []obsidian.Handler {
	return []obsidian.Handler{
		{Path: DevicesPath, Methods: obsidian.GET, HandlerFunc: h.listDevices},
		{Path: TasksPath, Methods: obsidian.GET, HandlerFunc: h.listTasks},
		{Path: TasksPath, Methods: obsidian.POST, HandlerFunc: h.createTask},
		{Path: TaskPath, Methods: obsidian.GET, HandlerFunc: h.getTask},
	}
}

func (h *Handlers) listDevices(c echo.Context) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	devices, err := h.store.ListDevices(networkID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]*models.AcsDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, (&models.AcsDevice{}).FromStorage(d))
	}
	return c.JSON(http.StatusOK, out)
}
