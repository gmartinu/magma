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
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/labstack/echo/v4"

	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/sessionlog"
	"magma/orc8r/cloud/go/services/obsidian"
)

const (
	LogsPath = ManageNetworkPath + obsidian.UrlSep + "logs"

	defaultLogsSize = 100
	maxLogsSize     = 1000
)

// LogSearcher reads the session log; sessionlog.Elastic in production.
type LogSearcher interface {
	Search(ctx context.Context, q sessionlog.Query) (*models.AcsLogs, error)
}

// WithLogs serves GET /acs/{network_id}/logs from s. Without it the route
// answers 503, so a controller without Elasticsearch still serves the rest.
func (h *Handlers) WithLogs(s LogSearcher) *Handlers {
	h.logs = s
	return h
}

func (h *Handlers) searchLogs(c echo.Context) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	if h.logs == nil {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "the session log search is not configured")
	}
	q, err := logQuery(c, networkID)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	page, err := h.logs.Search(c.Request().Context(), q)
	if errors.Is(err, sessionlog.ErrUnavailable) {
		return echo.NewHTTPError(http.StatusServiceUnavailable, "the session log search is unavailable: Elasticsearch cannot be reached")
	}
	if err != nil {
		return obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}
	return c.JSON(http.StatusOK, page)
}

func logQuery(c echo.Context, networkID string) (sessionlog.Query, error) {
	q := sessionlog.Query{
		NetworkID: networkID,
		CpeKey:    c.QueryParam("cpe_key"),
		GatewayID: c.QueryParam("gateway_id"),
		Event:     c.QueryParam("event"),
		Size:      defaultLogsSize,
	}
	switch q.Event {
	case "", models.AcsLogEventCpeSessionCompleted, models.AcsLogEventCpeTaskFailed:
	default:
		return q, fmt.Errorf("event must be %s or %s", models.AcsLogEventCpeSessionCompleted, models.AcsLogEventCpeTaskFailed)
	}
	var err error
	if q.Start, err = timeParam(c, "start"); err != nil {
		return q, err
	}
	if q.End, err = timeParam(c, "end"); err != nil {
		return q, err
	}
	if q.Start != nil && q.End != nil && q.End.Before(*q.Start) {
		return q, fmt.Errorf("end is before start")
	}
	if v := c.QueryParam("size"); v != "" {
		if q.Size, err = strconv.Atoi(v); err != nil || q.Size < 1 || q.Size > maxLogsSize {
			return q, fmt.Errorf("size must be an integer from 1 to %d", maxLogsSize)
		}
	}
	if v := c.QueryParam("from"); v != "" {
		if q.From, err = strconv.Atoi(v); err != nil || q.From < 0 {
			return q, fmt.Errorf("from must be a non-negative integer")
		}
	}
	if q.From+q.Size > sessionlog.MaxWindow {
		return q, fmt.Errorf("from + size cannot exceed %d; narrow the time range instead", sessionlog.MaxWindow)
	}
	return q, nil
}

func timeParam(c echo.Context, name string) (*time.Time, error) {
	v := c.QueryParam(name)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC 3339 time", name)
	}
	return &t, nil
}
