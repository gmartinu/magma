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

	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/sessionlog"
)

type fakeLogs struct {
	got  *sessionlog.Query
	page *models.AcsLogs
	err  error
}

func (f *fakeLogs) Search(_ context.Context, q sessionlog.Query) (*models.AcsLogs, error) {
	f.got = &q
	return f.page, f.err
}

func searchLogs(t *testing.T, h *Handlers, query string) (int, []byte) {
	rec := serve(t, h, http.MethodGet, LogsPath, "/magma/v1/acs/n1/logs"+query, map[string]string{"network_id": "n1"}, "")
	return rec.Code, rec.Body.Bytes()
}

func TestSearchLogsPassesTheFilters(t *testing.T) {
	logs := &fakeLogs{page: &models.AcsLogs{
		TotalCount: 7,
		Logs:       []*models.AcsLog{{Event: models.AcsLogEventCpeSessionCompleted, CpeKey: cpeKey, SessionID: "s1"}},
	}}
	h := newHandlers(nil).WithLogs(logs)

	code, body := searchLogs(t, h, "?cpe_key="+cpeKey+"&gateway_id=gw1&event=cpe_task_failed"+
		"&start=2026-10-06T10:00:00Z&end=2026-10-06T11:00:00Z&size=20&from=40")
	require.Equal(t, http.StatusOK, code, string(body))

	start, end := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC)
	require.NotNil(t, logs.got)
	assert.Equal(t, "n1", logs.got.NetworkID)
	assert.Equal(t, cpeKey, logs.got.CpeKey)
	assert.Equal(t, "gw1", logs.got.GatewayID)
	assert.Equal(t, models.AcsLogEventCpeTaskFailed, logs.got.Event)
	assert.True(t, start.Equal(*logs.got.Start))
	assert.True(t, end.Equal(*logs.got.End))
	assert.Equal(t, 20, logs.got.Size)
	assert.Equal(t, 40, logs.got.From)

	var page models.AcsLogs
	require.NoError(t, json.Unmarshal(body, &page))
	assert.Equal(t, int64(7), page.TotalCount)
	require.Len(t, page.Logs, 1)
	assert.Equal(t, "s1", page.Logs[0].SessionID)
}

func TestSearchLogsDefaults(t *testing.T) {
	logs := &fakeLogs{page: &models.AcsLogs{Logs: []*models.AcsLog{}}}
	code, _ := searchLogs(t, newHandlers(nil).WithLogs(logs), "")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, sessionlog.Query{NetworkID: "n1", Size: defaultLogsSize}, *logs.got)
}

func TestSearchLogsRefusesBadParameters(t *testing.T) {
	for _, query := range []string{
		"?event=inform",
		"?start=yesterday",
		"?end=2026-10-06",
		"?start=2026-10-06T11:00:00Z&end=2026-10-06T10:00:00Z",
		"?size=0",
		"?size=1001",
		"?size=ten",
		"?from=-1",
		"?from=9950&size=100",
	} {
		logs := &fakeLogs{}
		code, _ := searchLogs(t, newHandlers(nil).WithLogs(logs), query)
		assert.Equal(t, http.StatusBadRequest, code, query)
		assert.Nil(t, logs.got, query)
	}
}

func TestSearchLogsWithoutElasticsearch(t *testing.T) {
	code, _ := searchLogs(t, newHandlers(nil), "")
	assert.Equal(t, http.StatusServiceUnavailable, code)
}

func TestSearchLogsSearchError(t *testing.T) {
	code, _ := searchLogs(t, newHandlers(nil).WithLogs(&fakeLogs{err: errors.New("es down")}), "")
	assert.Equal(t, http.StatusInternalServerError, code)
}
