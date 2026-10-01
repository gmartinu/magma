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

package sessionlog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/olivere/elastic/v7"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/kpi"
	"magma/acs/cloud/go/services/acs/sessionlog"
)

func TestWriterSink(t *testing.T) {
	var buf bytes.Buffer
	s := sessionlog.NewWriterSink(&buf)
	s.Emit(&sessionlog.Record{TimestampMs: 5, Event: sessionlog.EventInform, DeviceID: "D-1", EventCodes: []string{"0 BOOTSTRAP"}})
	s.Emit(&sessionlog.Record{TimestampMs: 6, Event: sessionlog.EventSessionEnd, DeviceID: "D-1", RPCCount: 2, DurationMs: 3})
	assert.Equal(t,
		`{"timestamp_ms":5,"event":"inform","device_id":"D-1","event_codes":["0 BOOTSTRAP"],"duration_ms":0}`+"\n"+
			`{"timestamp_ms":6,"event":"session_end","device_id":"D-1","duration_ms":3,"rpc_count":2}`+"\n",
		buf.String())
}

type fluentd struct {
	mu     sync.Mutex
	bodies []string
	paths  []string
	status int
}

func (f *fluentd) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, string(b))
	f.paths = append(f.paths, r.URL.Path+" "+r.Header.Get("Content-Type"))
	w.WriteHeader(f.status)
}

func TestHTTPSink(t *testing.T) {
	fd := &fluentd{status: http.StatusOK}
	ts := httptest.NewServer(fd)
	defer ts.Close()

	s := sessionlog.NewHTTPSink(ts.URL+"/acs", 10, time.Second)
	s.Emit(&sessionlog.Record{TimestampMs: 1, Event: sessionlog.EventRPC, RPC: "GetParameterValues"})
	s.Close()
	assert.Equal(t, []string{`{"timestamp_ms":1,"event":"rpc","rpc":"GetParameterValues","duration_ms":0}`}, fd.bodies)
	assert.Equal(t, []string{"/acs application/json"}, fd.paths)

	dropped := testutil.ToFloat64(kpi.SessionLogsDropped)
	fd.status = http.StatusBadRequest
	s = sessionlog.NewHTTPSink(ts.URL+"/acs", 10, time.Second)
	s.Emit(&sessionlog.Record{Event: sessionlog.EventRPC})
	s.Close()
	assert.Equal(t, dropped+1, testutil.ToFloat64(kpi.SessionLogsDropped))
}

// stubElastic answers every search with the canned hits and keeps the
// request.
type stubElastic struct {
	path  string
	query map[string]interface{}
	hits  string
}

func (s *stubElastic) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.path = r.URL.Path
	_ = json.NewDecoder(r.Body).Decode(&s.query)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"took":1,"timed_out":false,"hits":{"total":{"value":7,"relation":"eq"},"hits":[` + s.hits + `]}}`))
}

func TestElasticSearcher(t *testing.T) {
	es := &stubElastic{hits: `{"_index":"acs-2026.10.01","_id":"a","_source":{"timestamp_ms":20,"event":"inform","device_id":"D-1","network_id":"net","session_id":"s1","source_ip":"10.0.0.2","event_codes":["2 PERIODIC"],"duration_ms":4}}`}
	ts := httptest.NewServer(es)
	defer ts.Close()
	searcher := sessionlog.NewElasticSearcher(func() (*elastic.Client, error) {
		return elastic.NewSimpleClient(elastic.SetURL(ts.URL))
	})

	page, err := searcher.Search(context.Background(), sessionlog.Query{NetworkID: "net", DeviceID: "D-1", BeginMs: 10, EndMs: 30, Size: 5})
	require.NoError(t, err)
	assert.Equal(t, int64(7), page.Total)
	assert.Equal(t, []*sessionlog.Record{{
		TimestampMs: 20, Event: "inform", DeviceID: "D-1", NetworkID: "net", SessionID: "s1", SourceIP: "10.0.0.2",
		EventCodes: []string{"2 PERIODIC"}, DurationMs: 4,
	}}, page.Records)

	assert.Equal(t, "/acs-*/_search", es.path)
	want := `{
		"query": {"bool": {"filter": [
			{"term": {"network_id.keyword": "net"}},
			{"term": {"device_id.keyword": "D-1"}},
			{"range": {"timestamp_ms": {"from": 10, "include_lower": true, "include_upper": true, "to": 30}}}
		]}},
		"size": 5,
		"sort": [{"timestamp_ms": {"order": "desc"}}]
	}`
	got, err := json.Marshal(es.query)
	require.NoError(t, err)
	assert.JSONEq(t, want, string(got))

	// Without filters only the network is matched.
	es.hits = ""
	page, err = searcher.Search(context.Background(), sessionlog.Query{NetworkID: "net", Size: 50})
	require.NoError(t, err)
	assert.Empty(t, page.Records)
	got, err = json.Marshal(es.query["query"])
	require.NoError(t, err)
	assert.JSONEq(t, `{"bool": {"filter": {"term": {"network_id.keyword": "net"}}}}`, string(got))

	_, err = searcher.Search(context.Background(), sessionlog.Query{})
	assert.Error(t, err)
}

func TestElasticSearcherRetriesClient(t *testing.T) {
	calls := 0
	searcher := sessionlog.NewElasticSearcher(func() (*elastic.Client, error) {
		calls++
		return nil, errors.New("no elastic config")
	})
	for n := 0; n < 2; n++ {
		_, err := searcher.Search(context.Background(), sessionlog.Query{NetworkID: "net"})
		require.Error(t, err)
		assert.True(t, strings.Contains(err.Error(), "no elastic config"))
	}
	assert.Equal(t, 2, calls)
}
