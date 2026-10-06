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

package sessionlog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/olivere/elastic/v7"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/obsidian/models"
)

const (
	sessionDoc = `{"stream_name":"acsd","event_type":"cpe_session_completed","event_tag":"IMSI001010000000001",
		"hw_id":"hw1","network_id":"n1","gateway_id":"gw1","@timestamp":"2026-10-06T10:00:05.250+00:00",
		"value":"{\"cpe_key\": \"IMSI001010000000001\", \"ended\": 1791280805.0, \"faults\": 1, \"imsi\": \"001010000000001\", \"reason\": \"\", \"result\": \"completed\", \"session_id\": \"s1\", \"started\": 1791280800.5, \"tasks_done\": 2, \"tasks_failed\": 0}"}`
	taskDoc = `{"stream_name":"acsd","event_type":"cpe_task_failed","event_tag":"CLAIMsim",
		"hw_id":"hw1","network_id":"n1","gateway_id":"gw1","@timestamp":"2026-10-06T09:00:00Z",
		"value":"{\"attempts\": 3, \"cpe_key\": \"CLAIMsim\", \"fault_code\": 9002, \"fault_string\": \"Internal error\", \"imsi\": \"\", \"max_attempts\": 3, \"task_id\": \"t1\", \"task_type\": \"reboot\"}"}`
)

func TestToLogSession(t *testing.T) {
	l, err := ToLog(json.RawMessage(sessionDoc))
	require.NoError(t, err)
	started := strfmt.DateTime(time.UnixMilli(1791280800500).UTC())
	ended := strfmt.DateTime(time.Unix(1791280805, 0).UTC())
	assert.Equal(t, &models.AcsLog{
		Time:       strfmt.DateTime(time.Date(2026, 10, 6, 10, 0, 5, 250e6, time.UTC)),
		Event:      models.AcsLogEventCpeSessionCompleted,
		CpeKey:     "IMSI001010000000001",
		Imsi:       "001010000000001",
		GatewayID:  "gw1",
		HardwareID: "hw1",
		SessionID:  "s1",
		Result:     "completed",
		Started:    &started,
		Ended:      &ended,
		TasksDone:  2,
		Faults:     1,
	}, l)
	assert.NoError(t, l.Validate(strfmt.Default))
}

func TestToLogTaskFailed(t *testing.T) {
	l, err := ToLog(json.RawMessage(taskDoc))
	require.NoError(t, err)
	assert.Equal(t, models.AcsLogEventCpeTaskFailed, l.Event)
	assert.Equal(t, "CLAIMsim", l.CpeKey)
	assert.Equal(t, "t1", l.TaskID)
	assert.Equal(t, "reboot", l.TaskType)
	assert.Equal(t, int32(3), l.Attempts)
	assert.Equal(t, int32(3), l.MaxAttempts)
	assert.Equal(t, int32(9002), l.FaultCode)
	assert.Equal(t, "Internal error", l.FaultString)
	assert.Nil(t, l.Started)
	assert.NoError(t, l.Validate(strfmt.Default))
}

func TestToLogFallsBackToTheTag(t *testing.T) {
	l, err := ToLog(json.RawMessage(`{"event_type":"cpe_session_completed","event_tag":"IMSI1","@timestamp":"2026-10-06T10:00:00Z","value":"{}"}`))
	require.NoError(t, err)
	assert.Equal(t, "IMSI1", l.CpeKey)
}

func TestToLogRefusesMalformedDocuments(t *testing.T) {
	for _, doc := range []string{
		`not json`,
		`{"event_type":"cpe_task_failed","@timestamp":"yesterday","value":"{}"}`,
		`{"event_type":"cpe_task_failed","@timestamp":"2026-10-06T10:00:00Z","value":"{"}`,
	} {
		_, err := ToLog(json.RawMessage(doc))
		assert.Error(t, err, doc)
	}
}

func TestBoolQuery(t *testing.T) {
	start, end := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC), time.Date(2026, 10, 6, 11, 0, 0, 0, time.UTC)
	src, err := Query{
		NetworkID: "n1", CpeKey: "IMSI1", GatewayID: "gw1", Event: "cpe_task_failed", Start: &start, End: &end,
	}.BoolQuery().Source()
	require.NoError(t, err)
	got, err := json.Marshal(src)
	require.NoError(t, err)
	assert.JSONEq(t, `{"bool":{"filter":[
		{"term":{"network_id.keyword":"n1"}},
		{"term":{"stream_name.keyword":"acsd"}},
		{"term":{"event_tag.keyword":"IMSI1"}},
		{"term":{"gateway_id.keyword":"gw1"}},
		{"term":{"event_type.keyword":"cpe_task_failed"}},
		{"range":{"@timestamp":{"from":"2026-10-06T10:00:00Z","include_lower":true,"include_upper":true,"to":"2026-10-06T11:00:00Z"}}}
	]}}`, string(got))
}

func TestBoolQueryOnlyScopesTheNetworkAndStream(t *testing.T) {
	src, err := Query{NetworkID: "n1"}.BoolQuery().Source()
	require.NoError(t, err)
	got, err := json.Marshal(src)
	require.NoError(t, err)
	assert.JSONEq(t, `{"bool":{"filter":[
		{"term":{"network_id.keyword":"n1"}},
		{"term":{"stream_name.keyword":"acsd"}}
	]}}`, string(got))
}

func TestSearch(t *testing.T) {
	var path string
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"hits":{"total":{"value":42,"relation":"eq"},"hits":[
			{"_id":"a","_source":`+sessionDoc+`},
			{"_id":"b","_source":{"event_type":"cpe_task_failed","@timestamp":"bad"}},
			{"_id":"c","_source":`+taskDoc+`}]}}`)
	}))
	defer srv.Close()
	client, err := elastic.NewSimpleClient(elastic.SetURL(srv.URL))
	require.NoError(t, err)

	page, err := NewElastic(client).Search(context.Background(), Query{NetworkID: "n1", From: 20, Size: 10})
	require.NoError(t, err)

	assert.Contains(t, []string{"/eventd*/_search", "/eventd%2A/_search"}, path)
	assert.Equal(t, float64(20), body["from"])
	assert.Equal(t, float64(10), body["size"])
	assert.Equal(t, true, body["track_total_hits"])
	assert.Equal(t, []interface{}{map[string]interface{}{"@timestamp": map[string]interface{}{"order": "desc"}}}, body["sort"])
	assert.Equal(t, int64(42), page.TotalCount)
	// The malformed hit is skipped, the others kept in order.
	require.Len(t, page.Logs, 2)
	assert.Equal(t, "s1", page.Logs[0].SessionID)
	assert.Equal(t, "t1", page.Logs[1].TaskID)
	assert.NoError(t, page.Validate(strfmt.Default))
}

func TestSearchElasticError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"search_phase_execution_exception","reason":"boom"},"status":400}`)
	}))
	defer srv.Close()
	client, err := elastic.NewSimpleClient(elastic.SetURL(srv.URL))
	require.NoError(t, err)

	_, err = NewElastic(client).Search(context.Background(), Query{NetworkID: "n1", Size: 10})
	assert.Error(t, err)
	assert.False(t, errors.Is(err, ErrUnavailable), "a rejected search is not an outage")
}

func searchAgainst(t *testing.T, url string, timeout time.Duration) error {
	client, err := elastic.NewSimpleClient(elastic.SetURL(url))
	require.NoError(t, err)
	_, err = NewElastic(client).WithTimeout(timeout).Search(context.Background(), Query{NetworkID: "n1", Size: 10})
	return err
}

func TestSearchUnavailable(t *testing.T) {
	refused := httptest.NewServer(http.NotFoundHandler())
	refusedURL := refused.URL
	refused.Close()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"type":"cluster_block_exception","reason":"blocked"},"status":503}`)
	}))
	defer failing.Close()

	block := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(block)

	for name, url := range map[string]string{
		"dns":     "http://elasticsearch.invalid:9200",
		"refused": refusedURL,
		"5xx":     failing.URL,
		"timeout": slow.URL,
	} {
		err := searchAgainst(t, url, 200*time.Millisecond)
		assert.True(t, errors.Is(err, ErrUnavailable), "%s: %v", name, err)
	}
}
