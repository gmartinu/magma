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
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/labstack/echo/v4"

	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/services/obsidian"
)

func (h *Handlers) listUnclaimed(c echo.Context) error {
	f, err := h.deviceFilter(c)
	if err != nil {
		return err
	}
	f.Unclaimed = true
	return h.findDevices(c, f)
}

func (h *Handlers) listDevices(c echo.Context) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	f, err := h.deviceFilter(c)
	if err != nil {
		return err
	}
	f.NetworkID = networkID
	return h.findDevices(c, f)
}

// deviceFilter reads the model and online query parameters.
func (h *Handlers) deviceFilter(c echo.Context) (storage.DeviceFilter, error) {
	f := storage.DeviceFilter{Model: c.QueryParam("model"), Policy: h.Online, NowSec: clock.Now().Unix()}
	if v := c.QueryParam("online"); v != "" {
		online, err := strconv.ParseBool(v)
		if err != nil {
			return f, echo.NewHTTPError(http.StatusBadRequest, "online must be true or false")
		}
		f.Online = &online
	}
	return f, nil
}

func (h *Handlers) findDevices(c echo.Context, f storage.DeviceFilter) error {
	devices, err := h.store.FindDevices(f)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	out := make([]*models.AcsDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, h.deviceModel(d, f.NowSec))
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handlers) deviceModel(d *storage.Device, nowSec int64) *models.AcsDevice {
	return (&models.AcsDevice{}).FromStorage(d, h.Online.Online(d, nowSec))
}

func (h *Handlers) getDevice(c echo.Context) error {
	d, err := h.device(c)
	if err != nil {
		return err
	}
	out := &models.AcsDeviceDetail{
		Device: *h.deviceModel(d, clock.Now().Unix()),
		LastInform: models.AcsInform{
			Time:   strfmt.DateTime(time.Unix(d.LastSeenSec, 0).UTC()),
			Events: append([]string{}, d.LastInformEvents...),
		},
	}
	st, serr := h.store.GetDeviceState(d.DeviceID)
	if serr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, serr.Error())
	}
	if st != nil {
		var state interface{}
		if err := json.Unmarshal([]byte(st.Model), &state); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		out.State = state
		updated := strfmt.DateTime(time.Unix(st.UpdatedSec, 0).UTC())
		out.StateUpdated = &updated
	}
	return c.JSON(http.StatusOK, out)
}

func (h *Handlers) claimDevice(c echo.Context) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	deviceID := c.Param("device_id")
	if deviceID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing device ID")
	}
	err := h.store.ClaimDevice(deviceID, networkID)
	switch {
	case errors.Is(err, storage.ErrDeviceNotFound):
		return echo.NewHTTPError(http.StatusNotFound, "device not found")
	case errors.Is(err, storage.ErrDeviceClaimed):
		// Also when another network holds it: which one is not revealed.
		return echo.NewHTTPError(http.StatusConflict, "device already claimed")
	case err != nil:
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	d, err := h.store.GetDevice(deviceID)
	if err != nil || d == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "claimed device disappeared")
	}
	return c.JSON(http.StatusOK, h.deviceModel(d, clock.Now().Unix()))
}

func (h *Handlers) listParameters(c echo.Context) error {
	d, err := h.device(c)
	if err != nil {
		return err
	}
	p, perr := h.store.GetParameters(d.DeviceID)
	if perr != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, perr.Error())
	}
	out := []*models.AcsParameter{}
	if p != nil {
		prefix := c.QueryParam("prefix")
		for name, v := range p.Values {
			if strings.HasPrefix(name, prefix) {
				out = append(out, &models.AcsParameter{
					Name: name, Value: v.Value, Updated: strfmt.DateTime(time.Unix(v.UpdatedSec, 0).UTC()),
				})
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	}
	return c.JSON(http.StatusOK, out)
}
