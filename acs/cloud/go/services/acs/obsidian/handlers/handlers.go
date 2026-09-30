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
	"time"

	"magma/acs/cloud/go/acs"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/services/obsidian"
)

const (
	// UnclaimedPath is served under its own obsidian path prefix: under
	// ManageNetworkPath it would be authorized as a network named
	// "unclaimed" instead of requiring access to every network.
	UnclaimedPath     = obsidian.V1Root + acs.ModuleName + obsidian.UrlSep + "unclaimed"
	ManageNetworkPath = obsidian.V1Root + acs.ModuleName + obsidian.UrlSep + ":network_id"
	DevicesPath       = ManageNetworkPath + obsidian.UrlSep + "devices"
	DevicePath        = DevicesPath + obsidian.UrlSep + ":device_id"
	ClaimPath         = DevicePath + obsidian.UrlSep + "claim"
	ParametersPath    = DevicePath + obsidian.UrlSep + "parameters"
	TasksPath         = DevicePath + obsidian.UrlSep + "tasks"
	TaskPath          = TasksPath + obsidian.UrlSep + ":task_id"
)

// PathPrefixes are the obsidian path prefixes the handlers are served under.
var PathPrefixes = []string{UnclaimedPath, ManageNetworkPath}

type Handlers struct {
	store storage.ACSStorage
	// TaskMaxAttempts and TaskTTL apply to tasks created without them.
	TaskMaxAttempts int
	TaskTTL         time.Duration
	// Online derives the online state of devices.
	Online storage.OnlinePolicy
}

func NewHandlers(store storage.ACSStorage) *Handlers {
	return &Handlers{store: store, TaskMaxAttempts: 3, TaskTTL: 7 * 24 * time.Hour, Online: storage.DefaultOnlinePolicy}
}

func (h *Handlers) GetHandlers() []obsidian.Handler {
	return []obsidian.Handler{
		{Path: UnclaimedPath, Methods: obsidian.GET, HandlerFunc: h.listUnclaimed},
		{Path: DevicesPath, Methods: obsidian.GET, HandlerFunc: h.listDevices},
		{Path: DevicePath, Methods: obsidian.GET, HandlerFunc: h.getDevice},
		{Path: ClaimPath, Methods: obsidian.POST, HandlerFunc: h.claimDevice},
		{Path: ParametersPath, Methods: obsidian.GET, HandlerFunc: h.listParameters},
		{Path: TasksPath, Methods: obsidian.GET, HandlerFunc: h.listTasks},
		{Path: TasksPath, Methods: obsidian.POST, HandlerFunc: h.createTask},
		{Path: TaskPath, Methods: obsidian.GET, HandlerFunc: h.getTask},
	}
}
