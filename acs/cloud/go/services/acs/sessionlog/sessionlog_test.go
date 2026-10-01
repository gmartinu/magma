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
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"

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
