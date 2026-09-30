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

package storage

import (
	"database/sql"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/sqorc"
)

func (s *sqlACSStorage) PutDeviceState(state *DeviceState) error {
	_, err := s.builder.Insert(deviceStateTable).
		Columns(deviceIDCol, handlerCol, modelJSONCol, updatedCol).
		Values(state.DeviceID, state.Handler, state.Model, state.UpdatedSec).
		OnConflict(
			[]sqorc.UpsertValue{
				{Column: handlerCol, Value: state.Handler},
				{Column: modelJSONCol, Value: state.Model},
				{Column: updatedCol, Value: state.UpdatedSec},
			},
			deviceIDCol,
		).
		RunWith(s.db).
		Exec()
	if err != nil {
		return fmt.Errorf("put state of %s: %w", state.DeviceID, err)
	}
	return nil
}

func (s *sqlACSStorage) GetDeviceState(deviceID string) (*DeviceState, error) {
	st := &DeviceState{}
	err := s.builder.Select(deviceIDCol, handlerCol, modelJSONCol, updatedCol).
		From(deviceStateTable).
		Where(sq.Eq{deviceIDCol: deviceID}).
		RunWith(s.db).
		QueryRow().
		Scan(&st.DeviceID, &st.Handler, &st.Model, &st.UpdatedSec)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get state of %s: %w", deviceID, err)
	}
	return st, nil
}

func (s *sqlACSStorage) GetOrCreateSecret(name string, generate func() (string, error)) (string, error) {
	get := func() (string, error) {
		var v string
		err := s.builder.Select(valueCol).
			From(secretsTable).
			Where(sq.Eq{nameCol: name}).
			RunWith(s.db).
			QueryRow().
			Scan(&v)
		if err == sql.ErrNoRows {
			return "", nil
		}
		return v, err
	}
	v, err := get()
	if err != nil || v != "" {
		return v, err
	}
	candidate, err := generate()
	if err != nil {
		return "", err
	}
	// Replicas starting together race here; DO NOTHING keeps the first secret
	// written and every replica reads that one back.
	_, err = s.builder.Insert(secretsTable).
		Columns(nameCol, valueCol).
		Values(name, candidate).
		OnConflict(nil, nameCol).
		RunWith(s.db).
		Exec()
	if err != nil {
		return "", fmt.Errorf("create secret %s: %w", name, err)
	}
	return get()
}

func (s *sqlACSStorage) CountInform(deviceID string, windowSec int64) (int, int64, error) {
	type out struct {
		count int
		end   int64
	}
	txFn := func(tx *sql.Tx) (interface{}, error) {
		now := clock.Now().Unix()
		var count int
		var end int64
		err := s.builder.Select(informCountCol, windowEndCol).
			From(informRateTbl).
			Where(sq.Eq{deviceIDCol: deviceID}).
			Suffix(sqorc.GetSqlLocker().WithLock()).
			RunWith(tx).
			QueryRow().
			Scan(&count, &end)
		if err != nil && err != sql.ErrNoRows {
			return nil, fmt.Errorf("select inform count: %w", err)
		}
		if err == sql.ErrNoRows || end <= now {
			count, end = 0, now+windowSec
		}
		count++
		_, err = s.builder.Insert(informRateTbl).
			Columns(deviceIDCol, informCountCol, windowEndCol).
			Values(deviceID, count, end).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: informCountCol, Value: count},
					{Column: windowEndCol, Value: end},
				},
				deviceIDCol,
			).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("count inform: %w", err)
		}
		return out{count, end}, nil
	}
	ret, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	if err != nil {
		return 0, 0, err
	}
	o := ret.(out)
	return o.count, o.end, nil
}
