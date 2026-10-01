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
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/sessionlog"
	"magma/orc8r/cloud/go/services/obsidian"
)

type fakeSearcher struct {
	queries []sessionlog.Query
	page    *sessionlog.Page
	err     error
}

func (s *fakeSearcher) Search(_ context.Context, q sessionlog.Query) (*sessionlog.Page, error) {
	s.queries = append(s.queries, q)
	return s.page, s.err
}

func TestListLogs(t *testing.T) {
	searcher := &fakeSearcher{page: &sessionlog.Page{Total: 12, Records: []*sessionlog.Record{
		{TimestampMs: 1790000000123, Event: sessionlog.EventInform, DeviceID: "D-1", NetworkID: "n1", SessionID: "s1",
			SourceIP: "198.51.100.7", EventCodes: []string{"2 PERIODIC"}, DurationMs: 9},
		{TimestampMs: 1790000000456, Event: sessionlog.EventRPC, DeviceID: "D-1", NetworkID: "n1", SessionID: "s1",
			RPC: "GetParameterValues", TaskID: "t1", TaskType: "refresh", FaultCode: 9005, FaultString: "Invalid parameter name"},
	}}}
	h := handlers.NewHandlers(newStore(t))
	h.Logs = searcher
	list := handler(t, h, handlers.LogsPath, obsidian.GET)

	status, body := call(t, list, http.MethodGet, "device_id=D-1&begin=2026-09-21T00:00:00Z&end=2026-09-22T00:00:00Z&size=2", "network_id", "n1")
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `{"total_count": 12, "logs": [
		{"time": "2026-09-21T14:13:20.123Z", "event": "inform", "device_id": "D-1", "network_id": "n1", "session_id": "s1",
		 "source_ip": "198.51.100.7", "event_codes": ["2 PERIODIC"], "duration_ms": 9},
		{"time": "2026-09-21T14:13:20.456Z", "event": "rpc", "device_id": "D-1", "network_id": "n1", "session_id": "s1",
		 "event_codes": null, "rpc": "GetParameterValues", "task_id": "t1", "task_type": "refresh", "fault_code": 9005,
		 "fault_string": "Invalid parameter name"}
	]}`, body)
	assert.Equal(t, sessionlog.Query{NetworkID: "n1", DeviceID: "D-1", BeginMs: 1789948800000, EndMs: 1790035200000, Size: 2}, searcher.queries[0])

	status, _ = call(t, list, http.MethodGet, "", "network_id", "n1")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, sessionlog.Query{NetworkID: "n1", Size: 50}, searcher.queries[1])

	for _, q := range []string{"size=0", "size=1001", "size=x", "begin=yesterday", "end=1", "begin=2026-09-22T00:00:00Z&end=2026-09-21T00:00:00Z"} {
		status, _ = call(t, list, http.MethodGet, q, "network_id", "n1")
		assert.Equal(t, http.StatusBadRequest, status, q)
	}

	searcher.err = errors.New("elastic down")
	status, msg := call(t, list, http.MethodGet, "", "network_id", "n1")
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Contains(t, msg, "elastic down")

	status, _ = call(t, handler(t, handlers.NewHandlers(newStore(t)), handlers.LogsPath, obsidian.GET), http.MethodGet, "", "network_id", "n1")
	assert.Equal(t, http.StatusServiceUnavailable, status)
}
