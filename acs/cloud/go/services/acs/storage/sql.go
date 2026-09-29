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
	"context"
	"database/sql"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/sqorc"
)

const (
	devicesTable      = "acs_devices"
	devicesNetworkIdx = "acs_devices_network_idx"

	deviceIDCol     = "device_id"
	networkIDCol    = "network_id"
	ouiCol          = "oui"
	productClassCol = "product_class"
	serialNumberCol = "serial_number"
	modelCol        = "model"
	firmwareCol     = "firmware"
	handlerCol      = "handler"
	connReqURLCol   = "connection_request_url"
	firstSeenCol    = "first_seen_sec"
	lastSeenCol     = "last_seen_sec"

	credentialsTable   = "acs_credentials"
	acsUsernameCol     = "acs_username"
	acsPasswordHashCol = "acs_password_hash"
	connReqUsernameCol = "conn_req_username"
	connReqPasswordCol = "conn_req_password"
	updatedCol         = "updated_sec"

	sessionsTable      = "acs_sessions"
	sessionsDeviceIdx  = "acs_sessions_device_idx"
	sessionsExpiresIdx = "acs_sessions_expires_idx"
	sessionIDCol       = "session_id"
	stepCol            = "step"
	createdCol         = "created_sec"
	expiresCol         = "expires_sec"
)

var deviceCols = []string{
	deviceIDCol, networkIDCol, ouiCol, productClassCol, serialNumberCol, modelCol,
	firmwareCol, handlerCol, connReqURLCol, firstSeenCol, lastSeenCol,
}

var credentialsCols = []string{
	deviceIDCol, acsUsernameCol, acsPasswordHashCol, connReqUsernameCol, connReqPasswordCol, updatedCol,
}

var sessionCols = []string{sessionIDCol, deviceIDCol, stepCol, createdCol, expiresCol}

type sqlACSStorage struct {
	db      *sql.DB
	builder sqorc.StatementBuilder
}

// NewSQLACSStorage returns an ACSStorage backed by the Orc8r SQL database.
func NewSQLACSStorage(db *sql.DB, builder sqorc.StatementBuilder) ACSStorage {
	return &sqlACSStorage{db: db, builder: builder}
}

func (s *sqlACSStorage) Init() error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		_, err := s.builder.CreateTable(devicesTable).
			IfNotExists().
			Column(deviceIDCol).Type(sqorc.ColumnTypeText).PrimaryKey().EndColumn().
			Column(networkIDCol).Type(sqorc.ColumnTypeText).EndColumn().
			Column(ouiCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(productClassCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(serialNumberCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(modelCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(firmwareCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(handlerCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(connReqURLCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(firstSeenCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Column(lastSeenCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", devicesTable, err)
		}
		_, err = s.builder.CreateIndex(devicesNetworkIdx).
			IfNotExists().
			On(devicesTable).
			Columns(networkIDCol).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s index: %w", devicesNetworkIdx, err)
		}

		_, err = s.builder.CreateTable(credentialsTable).
			IfNotExists().
			Column(deviceIDCol).Type(sqorc.ColumnTypeText).PrimaryKey().EndColumn().
			Column(acsUsernameCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(acsPasswordHashCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(connReqUsernameCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(connReqPasswordCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(updatedCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Unique(acsUsernameCol).
			ForeignKey(devicesTable, map[string]string{deviceIDCol: deviceIDCol}, sqorc.ColumnOnDeleteCascade).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", credentialsTable, err)
		}

		_, err = s.builder.CreateTable(sessionsTable).
			IfNotExists().
			Column(sessionIDCol).Type(sqorc.ColumnTypeText).PrimaryKey().EndColumn().
			Column(deviceIDCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(stepCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(createdCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Column(expiresCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			ForeignKey(devicesTable, map[string]string{deviceIDCol: deviceIDCol}, sqorc.ColumnOnDeleteCascade).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", sessionsTable, err)
		}
		for idx, col := range map[string]string{sessionsDeviceIdx: deviceIDCol, sessionsExpiresIdx: expiresCol} {
			_, err = s.builder.CreateIndex(idx).
				IfNotExists().
				On(sessionsTable).
				Columns(col).
				RunWith(tx).
				Exec()
			if err != nil {
				return nil, fmt.Errorf("create %s index: %w", idx, err)
			}
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, &sql.TxOptions{Isolation: sql.LevelSerializable}, nil, txFn)
	return err
}

func (s *sqlACSStorage) UpsertDevice(d *Device) error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		// network_id is left out of the update on purpose: which network owns
		// a device is decided by a claim, never by what the device reports.
		_, err := s.builder.Insert(devicesTable).
			Columns(deviceCols...).
			Values(d.DeviceID, toNullString(d.NetworkID), d.OUI, d.ProductClass, d.SerialNumber, d.Model,
				d.Firmware, d.Handler, d.ConnectionRequestURL, d.FirstSeenSec, d.LastSeenSec).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: modelCol, Value: d.Model},
					{Column: firmwareCol, Value: d.Firmware},
					{Column: handlerCol, Value: d.Handler},
					{Column: connReqURLCol, Value: d.ConnectionRequestURL},
					{Column: lastSeenCol, Value: d.LastSeenSec},
				},
				deviceIDCol,
			).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("upsert device %s: %w", d.DeviceID, err)
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) GetDevice(deviceID string) (*Device, error) {
	devices, err := s.selectDevices(sq.Eq{deviceIDCol: deviceID})
	if err != nil || len(devices) == 0 {
		return nil, err
	}
	return devices[0], nil
}

func (s *sqlACSStorage) ListDevices(networkID string) ([]*Device, error) {
	return s.selectDevices(sq.Eq{networkIDCol: networkID})
}

func (s *sqlACSStorage) selectDevices(where sq.Sqlizer) ([]*Device, error) {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		rows, err := s.builder.Select(deviceCols...).
			From(devicesTable).
			Where(where).
			OrderBy(deviceIDCol).
			RunWith(tx).
			Query()
		if err != nil {
			return nil, fmt.Errorf("select devices: %w", err)
		}
		defer sqorc.CloseRowsLogOnError(rows, "selectDevices")

		devices := []*Device{}
		for rows.Next() {
			d := &Device{}
			var networkID sql.NullString
			err = rows.Scan(&d.DeviceID, &networkID, &d.OUI, &d.ProductClass, &d.SerialNumber, &d.Model,
				&d.Firmware, &d.Handler, &d.ConnectionRequestURL, &d.FirstSeenSec, &d.LastSeenSec)
			if err != nil {
				return nil, fmt.Errorf("scan device: %w", err)
			}
			d.NetworkID = networkID.String
			devices = append(devices, d)
		}
		return devices, rows.Err()
	}
	ret, err := sqorc.ExecInTx(s.db, &sql.TxOptions{ReadOnly: true}, nil, txFn)
	if err != nil {
		return nil, err
	}
	return ret.([]*Device), nil
}

func (s *sqlACSStorage) PutCredentials(c *Credentials) error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		_, err := s.builder.Insert(credentialsTable).
			Columns(credentialsCols...).
			Values(c.DeviceID, c.ACSUsername, c.ACSPasswordHash, c.ConnReqUsername, c.ConnReqPassword, c.UpdatedSec).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: acsUsernameCol, Value: c.ACSUsername},
					{Column: acsPasswordHashCol, Value: c.ACSPasswordHash},
					{Column: connReqUsernameCol, Value: c.ConnReqUsername},
					{Column: connReqPasswordCol, Value: c.ConnReqPassword},
					{Column: updatedCol, Value: c.UpdatedSec},
				},
				deviceIDCol,
			).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("put credentials for %s: %w", c.DeviceID, err)
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) GetCredentialsByUsername(acsUsername string) (*Credentials, error) {
	c := &Credentials{}
	err := s.builder.Select(credentialsCols...).
		From(credentialsTable).
		Where(sq.Eq{acsUsernameCol: acsUsername}).
		RunWith(s.db).
		QueryRowContext(context.Background()).
		Scan(&c.DeviceID, &c.ACSUsername, &c.ACSPasswordHash, &c.ConnReqUsername, &c.ConnReqPassword, &c.UpdatedSec)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get credentials for %s: %w", acsUsername, err)
	}
	return c, nil
}

func (s *sqlACSStorage) PutSession(session *Session) error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		_, err := s.builder.Insert(sessionsTable).
			Columns(sessionCols...).
			Values(session.SessionID, session.DeviceID, session.Step, session.CreatedSec, session.ExpiresSec).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: stepCol, Value: session.Step},
					{Column: expiresCol, Value: session.ExpiresSec},
				},
				sessionIDCol,
			).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("put session for %s: %w", session.DeviceID, err)
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) GetSession(sessionID string) (*Session, error) {
	session := &Session{}
	err := s.builder.Select(sessionCols...).
		From(sessionsTable).
		Where(sq.And{
			sq.Eq{sessionIDCol: sessionID},
			sq.Gt{expiresCol: clock.Now().Unix()},
		}).
		RunWith(s.db).
		QueryRowContext(context.Background()).
		Scan(&session.SessionID, &session.DeviceID, &session.Step, &session.CreatedSec, &session.ExpiresSec)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return session, nil
}

func (s *sqlACSStorage) DeleteExpiredSessions() error {
	_, err := s.builder.Delete(sessionsTable).
		Where(sq.LtOrEq{expiresCol: clock.Now().Unix()}).
		RunWith(s.db).
		Exec()
	if err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}

func toNullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
