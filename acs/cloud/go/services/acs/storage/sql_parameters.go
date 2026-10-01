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
	"encoding/json"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"magma/orc8r/cloud/go/sqorc"
)

const (
	parametersTable = "acs_parameters"
	paramsCol       = "params"
)

// parametersDDL is written by hand because sqorc has no JSON column type.
func (s *sqlACSStorage) parametersDDL() string {
	jsonType := "JSONB"
	if s.sqlite {
		jsonType = "TEXT"
	}
	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (%s TEXT PRIMARY KEY REFERENCES %s (%s) ON DELETE CASCADE, %s %s NOT NULL, %s BIGINT NOT NULL)",
		parametersTable, deviceIDCol, devicesTable, deviceIDCol, paramsCol, jsonType, updatedCol,
	)
}

func (s *sqlACSStorage) MergeParameters(deviceID string, values map[string]string, nowSec int64) error {
	if len(values) == 0 {
		return nil
	}
	txFn := func(tx *sql.Tx) (interface{}, error) {
		stored, err := s.selectParameters(tx, deviceID, true)
		if err != nil {
			return nil, err
		}
		merged := map[string]ParameterValue{}
		if stored != nil {
			merged = stored.Values
		}
		for name, v := range values {
			merged[name] = ParameterValue{Value: v, UpdatedSec: nowSec}
		}
		b, err := json.Marshal(merged)
		if err != nil {
			return nil, err
		}
		_, err = s.builder.Insert(parametersTable).
			Columns(deviceIDCol, paramsCol, updatedCol).
			Values(deviceID, string(b), nowSec).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: paramsCol, Value: string(b)},
					{Column: updatedCol, Value: nowSec},
				},
				deviceIDCol,
			).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("merge parameters of %s: %w", deviceID, err)
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) GetParameters(deviceID string) (*Parameters, error) {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		return s.selectParameters(tx, deviceID, false)
	}
	ret, err := sqorc.ExecInTx(s.db, &sql.TxOptions{ReadOnly: true}, nil, txFn)
	if err != nil {
		return nil, err
	}
	return ret.(*Parameters), nil
}

func (s *sqlACSStorage) selectParameters(tx *sql.Tx, deviceID string, lock bool) (*Parameters, error) {
	q := s.builder.Select(deviceIDCol, paramsCol, updatedCol).
		From(parametersTable).
		Where(sq.Eq{deviceIDCol: deviceID})
	if lock {
		q = q.Suffix(sqorc.GetSqlLocker().WithLock())
	}
	p := &Parameters{}
	var doc string
	err := q.RunWith(tx).QueryRow().Scan(&p.DeviceID, &doc, &p.UpdatedSec)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select parameters of %s: %w", deviceID, err)
	}
	if err := json.Unmarshal([]byte(doc), &p.Values); err != nil {
		return nil, fmt.Errorf("parameters of %s: %w", deviceID, err)
	}
	return p, nil
}
