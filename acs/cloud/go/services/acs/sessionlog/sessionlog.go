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

// Package sessionlog records one document per CWMP exchange and ships it to
// Elasticsearch through fluentd, the way the Domain Proxy ships its logs.
package sessionlog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/golang/glog"

	"magma/acs/cloud/go/services/acs/kpi"
)

// Kinds of record.
const (
	// EventInform is an accepted Inform, which opens a session.
	EventInform = "inform"
	// EventRPC is one RPC of a session: a CPE request the ACS answered, or
	// a CPE answer to an ACS request.
	EventRPC = "rpc"
	// EventSessionEnd is the ACS closing a session with nothing left to send.
	EventSessionEnd = "session_end"
	// EventAuthFailure is a request refused for its credentials.
	EventAuthFailure = "auth_failure"
	// EventRateLimited is an Inform refused by the session rate limit.
	EventRateLimited = "rate_limited"
)

// Record is one session log document. Its JSON is the schema of the acs-*
// indices; fields are only ever added to it.
type Record struct {
	// TimestampMs is when the ACS handled the request, in Unix milliseconds.
	TimestampMs int64  `json:"timestamp_ms"`
	Event       string `json:"event"`
	DeviceID    string `json:"device_id,omitempty"`
	// NetworkID is empty while the device is unclaimed.
	NetworkID  string   `json:"network_id,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
	SourceIP   string   `json:"source_ip,omitempty"`
	EventCodes []string `json:"event_codes,omitempty"`
	Handler    string   `json:"handler,omitempty"`
	Bootstrap  bool     `json:"bootstrap,omitempty"`
	// RPC is the method of an EventRPC: the CPE request, or the ACS request
	// the CPE answered.
	RPC         string `json:"rpc,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	TaskType    string `json:"task_type,omitempty"`
	FaultCode   int    `json:"fault_code,omitempty"`
	FaultString string `json:"fault_string,omitempty"`
	// Reason explains an auth failure or rate limit.
	Reason string `json:"reason,omitempty"`
	// DurationMs is how long the ACS took to handle the request.
	DurationMs int64 `json:"duration_ms"`
	// SessionDurationSec and RPCCount describe the whole session on
	// EventSessionEnd.
	SessionDurationSec int64 `json:"session_duration_sec,omitempty"`
	RPCCount           int   `json:"rpc_count,omitempty"`
}

// Sink receives the records. Emit must not block the CWMP exchange.
type Sink interface {
	Emit(r *Record)
}

// WriterSink writes one JSON record per line, for a log collector tailing
// the output of the service.
type WriterSink struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriterSink(w io.Writer) *WriterSink {
	return &WriterSink{w: w}
}

func (s *WriterSink) Emit(r *Record) {
	b, err := json.Marshal(r)
	if err != nil {
		kpi.SessionLogsDropped.Inc()
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.w.Write(append(b, '\n')); err != nil {
		kpi.SessionLogsDropped.Inc()
	}
}

// HTTPSink posts each record as JSON to a fluentd in_http source; the URL
// path is the fluentd tag (http://fluentd:9888/acs). Records are queued and
// sent by one goroutine; when the queue is full they are dropped and counted.
type HTTPSink struct {
	url    string
	client *http.Client
	queue  chan *Record
	done   chan struct{}
	// failing mutes the warnings of a failure streak after the first.
	failing bool
}

func NewHTTPSink(url string, queueSize int, timeout time.Duration) *HTTPSink {
	s := &HTTPSink{
		url:    url,
		client: &http.Client{Timeout: timeout},
		queue:  make(chan *Record, queueSize),
		done:   make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *HTTPSink) Emit(r *Record) {
	select {
	case s.queue <- r:
	default:
		kpi.SessionLogsDropped.Inc()
	}
}

// Close sends what is queued and stops the sink.
func (s *HTTPSink) Close() {
	close(s.queue)
	<-s.done
}

func (s *HTTPSink) run() {
	defer close(s.done)
	for r := range s.queue {
		err := s.post(r)
		switch {
		case err != nil:
			kpi.SessionLogsDropped.Inc()
			if !s.failing {
				glog.Warningf("send acs session logs to %s: %s; further failures are only counted", s.url, err)
			}
			s.failing = true
		case s.failing:
			glog.Infof("sending acs session logs to %s again", s.url)
			s.failing = false
		}
	}
}

func (s *HTTPSink) post(r *Record) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, s.url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("fluentd answered %s", resp.Status)
	}
	return nil
}
