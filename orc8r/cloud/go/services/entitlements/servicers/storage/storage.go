/*
 * Copyright 2026 The Magma Authors.
 *
 * This source code is licensed under the BSD-style license found in the
 * LICENSE file in the root directory of this source tree.
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
// Package storage keeps entitlement records in a blobstore table of the
// shared Orc8r DB: one blob per (tenant, feature), the tenant ID as the
// blob's network ID.
package storage

import (
	"sort"
	"strconv"

	"google.golang.org/protobuf/proto"

	"magma/orc8r/cloud/go/blobstore"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/protos"
	"magma/orc8r/cloud/go/storage"
)

type Store interface {
	// List returns the tenant's entitlements, sorted by feature.
	List(tenantID int64) ([]*protos.Entitlement, error)
	// Get returns merrors.ErrNotFound when the tenant has none.
	Get(tenantID int64, feature string) (*protos.Entitlement, error)
	// Set writes ents; replace also deletes the tenant's other features.
	Set(tenantID int64, ents []*protos.Entitlement, replace bool) error
	Delete(tenantID int64, feature string) error
}

type blobStore struct {
	factory blobstore.StoreFactory
}

func NewBlobstoreStore(factory blobstore.StoreFactory) Store {
	return &blobStore{factory: factory}
}

func tenantKey(tenantID int64) string {
	return strconv.FormatInt(tenantID, 10)
}

func tk(feature string) storage.TK {
	return storage.TK{Type: entitlements.EntitlementType, Key: feature}
}

func (s *blobStore) List(tenantID int64) ([]*protos.Entitlement, error) {
	tx, err := s.factory.StartTransaction(&storage.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	blobs, err := blobstore.GetAllOfType(tx, tenantKey(tenantID), entitlements.EntitlementType)
	if err != nil {
		return nil, err
	}
	ents := make([]*protos.Entitlement, 0, len(blobs))
	for _, b := range blobs {
		e := &protos.Entitlement{}
		if err := proto.Unmarshal(b.Value, e); err != nil {
			return nil, err
		}
		ents = append(ents, e)
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Feature < ents[j].Feature })
	return ents, tx.Commit()
}

func (s *blobStore) Get(tenantID int64, feature string) (*protos.Entitlement, error) {
	tx, err := s.factory.StartTransaction(&storage.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	b, err := tx.Get(tenantKey(tenantID), tk(feature))
	if err != nil {
		return nil, err
	}
	e := &protos.Entitlement{}
	if err := proto.Unmarshal(b.Value, e); err != nil {
		return nil, err
	}
	return e, tx.Commit()
}

func (s *blobStore) Set(tenantID int64, ents []*protos.Entitlement, replace bool) error {
	tx, err := s.factory.StartTransaction(nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := tenantKey(tenantID)
	blobs := make(blobstore.Blobs, 0, len(ents))
	keep := map[string]bool{}
	for _, e := range ents {
		value, err := proto.Marshal(e)
		if err != nil {
			return err
		}
		blobs = append(blobs, blobstore.Blob{Type: entitlements.EntitlementType, Key: e.Feature, Value: value})
		keep[e.Feature] = true
	}
	if replace {
		existing, err := blobstore.ListKeys(tx, key, entitlements.EntitlementType)
		if err != nil {
			return err
		}
		var drop storage.TKs
		for _, feature := range existing {
			if !keep[feature] {
				drop = append(drop, tk(feature))
			}
		}
		if len(drop) > 0 {
			if err := tx.Delete(key, drop); err != nil {
				return err
			}
		}
	}
	if len(blobs) > 0 {
		if err := tx.Write(key, blobs); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *blobStore) Delete(tenantID int64, feature string) error {
	tx, err := s.factory.StartTransaction(nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.Delete(tenantKey(tenantID), storage.TKs{tk(feature)}); err != nil {
		return err
	}
	return tx.Commit()
}
