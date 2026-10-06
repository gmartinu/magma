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

// Package reports keeps the cpe_acs state of each CPE per reporting
// gateway. The Orc8r state store keys a state by (network, type, key), so
// two gateways reporting one cpe_key overwrite each other's row every
// minute: an IMSI that moved to another AGW, which acsd on the old one
// keeps reporting (offline) for 7 days, or a claim ID used on two AGWs.
// The acs service indexes cpe_acs into this store, keyed by reporter as
// well, and resolves which gateway serves the CPE (Resolve).
package reports

import (
	"encoding/json"
	"fmt"
	"sort"

	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/orc8r/cloud/go/blobstore"
	"magma/orc8r/cloud/go/storage"
)

// TableName is the blobstore table of the reports, in the shared Orc8r DB.
const TableName = "acs_cpe_reports"

// Report is the state one gateway last reported for a CPE.
type Report struct {
	// HardwareID of the reporting gateway.
	HardwareID string
	// TimeMs is when the state service received it.
	TimeMs uint64
	View   *cpestate.CpeView
}

// Store keeps the reports; the blob type is the cpe_key and the blob key
// the reporter's hardware ID.
type Store struct {
	factory blobstore.StoreFactory
}

func NewStore(factory blobstore.StoreFactory) *Store {
	return &Store{factory: factory}
}

type value struct {
	TimeMs uint64          `json:"time_ms"`
	View   json.RawMessage `json:"view"`
}

// Put writes reports, replacing what their reporters said before.
func (s *Store) Put(networkID string, reports []Report) error {
	blobs := blobstore.Blobs{}
	for _, r := range reports {
		view, err := json.Marshal(r.View)
		if err != nil {
			return err
		}
		v, err := json.Marshal(value{TimeMs: r.TimeMs, View: view})
		if err != nil {
			return err
		}
		blobs = append(blobs, blobstore.Blob{Type: r.View.CpeKey, Key: r.HardwareID, Value: v})
	}
	return s.tx(func(store blobstore.Store) error { return store.Write(networkID, blobs) })
}

// Delete drops what hwID reported for cpeKey.
func (s *Store) Delete(networkID, cpeKey, hwID string) error {
	return s.tx(func(store blobstore.Store) error {
		return store.Delete(networkID, storage.TKs{{Type: cpeKey, Key: hwID}})
	})
}

// List returns the reports of every CPE of the network by cpe_key.
func (s *Store) List(networkID string) (map[string][]Report, error) {
	return s.search(blobstore.CreateSearchFilter(&networkID, nil, nil, nil))
}

// Get returns the reports of one CPE; none when no gateway reports it.
func (s *Store) Get(networkID, cpeKey string) ([]Report, error) {
	byKey, err := s.search(blobstore.CreateSearchFilter(&networkID, []string{cpeKey}, nil, nil))
	return byKey[cpeKey], err
}

func (s *Store) search(filter blobstore.SearchFilter) (map[string][]Report, error) {
	out := map[string][]Report{}
	err := s.tx(func(store blobstore.Store) error {
		byNetwork, err := store.Search(filter, blobstore.GetDefaultLoadCriteria())
		if err != nil {
			return err
		}
		for _, blobs := range byNetwork {
			for _, b := range blobs {
				var v value
				view := &cpestate.CpeView{}
				if err := json.Unmarshal(b.Value, &v); err != nil {
					return fmt.Errorf("report of %s by %s: %w", b.Type, b.Key, err)
				}
				if err := json.Unmarshal(v.View, view); err != nil {
					return fmt.Errorf("report of %s by %s: %w", b.Type, b.Key, err)
				}
				out[b.Type] = append(out[b.Type], Report{HardwareID: b.Key, TimeMs: v.TimeMs, View: view})
			}
		}
		return nil
	})
	return out, err
}

func (s *Store) tx(f func(blobstore.Store) error) error {
	store, err := s.factory.StartTransaction(nil)
	if err != nil {
		return err
	}
	if err := f(store); err != nil {
		_ = store.Rollback()
		return err
	}
	return store.Commit()
}

// Resolve picks the report of the gateway serving the CPE: the one with the
// newest Inform, as a CPE informs only the ACS it is attached to; then the
// newest report, then the lowest hardware ID, so the answer is the same on
// every call. The others are returned newest Inform first.
func Resolve(reports []Report) (Report, []Report) {
	sorted := append([]Report(nil), reports...)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.View.LastInform != b.View.LastInform {
			return a.View.LastInform > b.View.LastInform
		}
		if a.TimeMs != b.TimeMs {
			return a.TimeMs > b.TimeMs
		}
		return a.HardwareID < b.HardwareID
	})
	return sorted[0], sorted[1:]
}
