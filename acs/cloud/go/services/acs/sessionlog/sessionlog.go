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

// Package sessionlog searches the session log of the ACS: the eventd events
// acsd sends (lte/swagger/cpe_acs_events.v1.yml), which the gateway's
// td-agent-bit forwards to the Orc8r fluentd and fluentd indexes into the
// eventd-* Elasticsearch indices (orc8r/cloud/docker/fluentd/conf).
package sessionlog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/golang/glog"
	"github.com/olivere/elastic/v7"

	"magma/acs/cloud/go/services/acs/obsidian/models"
)

const (
	// StreamName is acsd's eventd stream.
	StreamName = "acsd"

	index = "eventd*"
	// Elasticsearch's default index.max_result_window: from+size past it
	// is refused.
	MaxWindow = 10000

	// Exact-match fields of the dynamic mapping. event_tag is eventd's tag
	// (fluentd keeps its own in tag); network_id and gateway_id are the
	// td-agent-bit extra tags the Orc8r mconfig builder sets.
	fieldNetworkID = "network_id.keyword"
	fieldGatewayID = "gateway_id.keyword"
	fieldStream    = "stream_name.keyword"
	fieldEventType = "event_type.keyword"
	fieldTag       = "event_tag.keyword"
	fieldTimestamp = "@timestamp"

	// SearchTimeout bounds one search, so an Elasticsearch that does not
	// answer fails the request instead of holding it open.
	SearchTimeout = 10 * time.Second
)

// ErrUnavailable wraps the errors that mean Elasticsearch could not be
// reached or could not serve the search: the log is down, not the query.
var ErrUnavailable = errors.New("elasticsearch is unavailable")

// Query selects records of one network; zero fields do not filter.
type Query struct {
	NetworkID string
	CpeKey    string
	GatewayID string
	Event     string
	Start     *time.Time
	End       *time.Time
	From      int
	Size      int
}

// BoolQuery is the Elasticsearch filter of q.
func (q Query) BoolQuery() *elastic.BoolQuery {
	b := elastic.NewBoolQuery().Filter(
		elastic.NewTermQuery(fieldNetworkID, q.NetworkID),
		elastic.NewTermQuery(fieldStream, StreamName),
	)
	if q.CpeKey != "" {
		b.Filter(elastic.NewTermQuery(fieldTag, q.CpeKey))
	}
	if q.GatewayID != "" {
		b.Filter(elastic.NewTermQuery(fieldGatewayID, q.GatewayID))
	}
	if q.Event != "" {
		b.Filter(elastic.NewTermQuery(fieldEventType, q.Event))
	}
	if q.Start != nil || q.End != nil {
		r := elastic.NewRangeQuery(fieldTimestamp)
		if q.Start != nil {
			r.Gte(q.Start.UTC().Format(time.RFC3339Nano))
		}
		if q.End != nil {
			r.Lte(q.End.UTC().Format(time.RFC3339Nano))
		}
		b.Filter(r)
	}
	return b
}

// Elastic searches the records in Elasticsearch.
type Elastic struct {
	client  *elastic.Client
	timeout time.Duration
}

func NewElastic(client *elastic.Client) *Elastic {
	return &Elastic{client: client, timeout: SearchTimeout}
}

// WithTimeout replaces SearchTimeout.
func (e *Elastic) WithTimeout(d time.Duration) *Elastic {
	e.timeout = d
	return e
}

// Search returns one page of q, newest first, and how many records match.
// Errors that mean Elasticsearch is down wrap ErrUnavailable.
func (e *Elastic) Search(ctx context.Context, q Query) (*models.AcsLogs, error) {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	res, err := e.client.Search().
		Index(index).
		Query(q.BoolQuery()).
		Sort(fieldTimestamp, false).
		From(q.From).
		Size(q.Size).
		TrackTotalHits(true).
		Do(ctx)
	if err != nil {
		if unavailable(err) {
			return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
		return nil, err
	}
	if res.Error != nil {
		return nil, fmt.Errorf("elasticsearch: %s: %s", res.Error.Type, res.Error.Reason)
	}
	out := &models.AcsLogs{Logs: []*models.AcsLog{}}
	if res.Hits == nil {
		return out, nil
	}
	if res.Hits.TotalHits != nil {
		out.TotalCount = res.Hits.TotalHits.Value
	}
	for _, hit := range res.Hits.Hits {
		l, err := ToLog(hit.Source)
		if err != nil {
			// One malformed document must not hide the rest of the log.
			glog.Warningf("Skipping session log record %s: %s", hit.Id, err)
			continue
		}
		out.Logs = append(out.Logs, l)
	}
	return out, nil
}

// unavailable tells whether err means Elasticsearch could not be reached
// (no live node, DNS failure, refused connection, timeout) or failed on its
// side (5xx), as opposed to a search it rejected.
func unavailable(err error) bool {
	if elastic.IsConnErr(err) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var esErr *elastic.Error
	return errors.As(err, &esErr) && esErr.Status >= http.StatusInternalServerError
}

type document struct {
	EventType  string `json:"event_type"`
	Tag        string `json:"event_tag"`
	HardwareID string `json:"hw_id"`
	GatewayID  string `json:"gateway_id"`
	Timestamp  string `json:"@timestamp"`
	// eventd's JSON-encoded value.
	Value string `json:"value"`
}

type value struct {
	CpeKey      string  `json:"cpe_key"`
	Imsi        string  `json:"imsi"`
	SessionID   string  `json:"session_id"`
	Result      string  `json:"result"`
	Reason      string  `json:"reason"`
	Started     float64 `json:"started"`
	Ended       float64 `json:"ended"`
	TasksDone   int32   `json:"tasks_done"`
	TasksFailed int32   `json:"tasks_failed"`
	Faults      int32   `json:"faults"`
	TaskID      string  `json:"task_id"`
	TaskType    string  `json:"task_type"`
	Attempts    int32   `json:"attempts"`
	MaxAttempts int32   `json:"max_attempts"`
	FaultCode   int32   `json:"fault_code"`
	FaultString string  `json:"fault_string"`
}

// ToLog turns one indexed eventd document into a record.
func ToLog(source json.RawMessage) (*models.AcsLog, error) {
	var d document
	if err := json.Unmarshal(source, &d); err != nil {
		return nil, err
	}
	at, err := time.Parse(time.RFC3339Nano, d.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("bad @timestamp %q", d.Timestamp)
	}
	var v value
	if d.Value != "" {
		if err := json.Unmarshal([]byte(d.Value), &v); err != nil {
			return nil, fmt.Errorf("bad value: %w", err)
		}
	}
	cpeKey := v.CpeKey
	if cpeKey == "" {
		cpeKey = d.Tag
	}
	return &models.AcsLog{
		Time:        strfmt.DateTime(at.UTC()),
		Event:       d.EventType,
		CpeKey:      cpeKey,
		Imsi:        v.Imsi,
		GatewayID:   d.GatewayID,
		HardwareID:  d.HardwareID,
		SessionID:   v.SessionID,
		Result:      v.Result,
		Reason:      v.Reason,
		Started:     unixTime(v.Started),
		Ended:       unixTime(v.Ended),
		TasksDone:   v.TasksDone,
		TasksFailed: v.TasksFailed,
		Faults:      v.Faults,
		TaskID:      v.TaskID,
		TaskType:    v.TaskType,
		Attempts:    v.Attempts,
		MaxAttempts: v.MaxAttempts,
		FaultCode:   v.FaultCode,
		FaultString: v.FaultString,
	}, nil
}

func unixTime(sec float64) *strfmt.DateTime {
	if sec <= 0 {
		return nil
	}
	t := strfmt.DateTime(time.UnixMilli(int64(sec * 1000)).UTC())
	return &t
}
