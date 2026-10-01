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
	"fmt"
	"sync"

	"github.com/olivere/elastic/v7"
)

const (
	// Index matches the daily indices fluentd writes the acs tag to.
	Index = "acs-*"

	timestampField = "timestamp_ms"
	// Dynamically mapped strings are text with an exact-match keyword
	// sub-field, as in the eventd indices.
	networkField = "network_id.keyword"
	deviceField  = "device_id.keyword"
)

// Query selects the records of one network, newest first.
type Query struct {
	NetworkID string
	// DeviceID, when set, keeps the records of that device.
	DeviceID string
	// BeginMs and EndMs, when non-zero, bound TimestampMs (inclusive).
	BeginMs int64
	EndMs   int64
	Size    int
}

// Page is the result of a query.
type Page struct {
	// Total counts every matching record, beyond Size.
	Total   int64
	Records []*Record
}

// Searcher reads the session logs back.
type Searcher interface {
	Search(ctx context.Context, q Query) (*Page, error)
}

// ClientGetter returns the Elasticsearch client.
type ClientGetter func() (*elastic.Client, error)

// ElasticSearcher queries the acs-* indices. The client is created on first
// use and again after a failure, so the service starts while Elasticsearch
// is down or not configured.
type ElasticSearcher struct {
	get    ClientGetter
	mu     sync.Mutex
	client *elastic.Client
}

func NewElasticSearcher(get ClientGetter) *ElasticSearcher {
	return &ElasticSearcher{get: get}
}

func (s *ElasticSearcher) getClient() (*elastic.Client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.client == nil {
		c, err := s.get()
		if err != nil {
			return nil, err
		}
		s.client = c
	}
	return s.client, nil
}

func (s *ElasticSearcher) Search(ctx context.Context, q Query) (*Page, error) {
	if q.NetworkID == "" {
		return nil, fmt.Errorf("search session logs: network ID required")
	}
	client, err := s.getClient()
	if err != nil {
		return nil, fmt.Errorf("elasticsearch client: %w", err)
	}
	res, err := client.Search(Index).
		Query(q.toElastic()).
		Sort(timestampField, false).
		Size(q.Size).
		Do(ctx)
	if err != nil {
		return nil, err
	}
	page := &Page{Total: res.TotalHits(), Records: []*Record{}}
	for _, hit := range res.Hits.Hits {
		r := &Record{}
		if err := json.Unmarshal(hit.Source, r); err != nil {
			return nil, fmt.Errorf("decode session log %s: %w", hit.Id, err)
		}
		page.Records = append(page.Records, r)
	}
	return page, nil
}

func (q Query) toElastic() elastic.Query {
	b := elastic.NewBoolQuery().Filter(elastic.NewTermQuery(networkField, q.NetworkID))
	if q.DeviceID != "" {
		b.Filter(elastic.NewTermQuery(deviceField, q.DeviceID))
	}
	if q.BeginMs != 0 || q.EndMs != 0 {
		r := elastic.NewRangeQuery(timestampField)
		if q.BeginMs != 0 {
			r.Gte(q.BeginMs)
		}
		if q.EndMs != 0 {
			r.Lte(q.EndMs)
		}
		b.Filter(r)
	}
	return b
}
