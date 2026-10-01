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
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/sessionlog"
	"magma/orc8r/cloud/go/services/obsidian"
)

const (
	defaultLogsSize = 50
	maxLogsSize     = 1000
)

func (h *Handlers) listLogs(c echo.Context) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	if h.Logs == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "session logs are not configured")
	}
	q := sessionlog.Query{NetworkID: networkID, DeviceID: c.QueryParam("device_id"), Size: defaultLogsSize}
	var err error
	if q.BeginMs, err = timeParam(c, "begin"); err != nil {
		return err
	}
	if q.EndMs, err = timeParam(c, "end"); err != nil {
		return err
	}
	if q.BeginMs != 0 && q.EndMs != 0 && q.BeginMs > q.EndMs {
		return echo.NewHTTPError(http.StatusBadRequest, "begin is after end")
	}
	if v := c.QueryParam("size"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLogsSize {
			return echo.NewHTTPError(http.StatusBadRequest, "size must be between 1 and 1000")
		}
		q.Size = n
	}

	page, err := h.Logs.Search(c.Request().Context(), q)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	total := page.Total
	out := models.AcsSessionLogs{TotalCount: &total, Logs: make([]models.AcsSessionLog, 0, len(page.Records))}
	for _, r := range page.Records {
		out.Logs = append(out.Logs, *(&models.AcsSessionLog{}).FromRecord(r))
	}
	return c.JSON(http.StatusOK, out)
}

// timeParam reads an RFC 3339 query parameter as Unix milliseconds, 0 when
// absent.
func timeParam(c echo.Context, name string) (int64, error) {
	v := c.QueryParam(name)
	if v == "" {
		return 0, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return 0, echo.NewHTTPError(http.StatusBadRequest, name+" must be an RFC 3339 time")
	}
	return t.UnixMilli(), nil
}
