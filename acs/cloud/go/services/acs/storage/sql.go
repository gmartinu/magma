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
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	sq "github.com/Masterminds/squirrel"

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
	intervalCol     = "inform_interval_sec"
	eventsCol       = "last_inform_events"

	credentialsTable   = "acs_credentials"
	acsUsernameCol     = "acs_username"
	acsPasswordHashCol = "acs_password_hash"
	pendingHashCol     = "pending_password_hash"
	connReqUsernameCol = "conn_req_username"
	connReqPasswordCol = "conn_req_password"
	updatedCol         = "updated_sec"

	sessionsTable      = "acs_sessions"
	sessionsDeviceIdx  = "acs_sessions_device_idx"
	sessionsExpiresIdx = "acs_sessions_expires_idx"
	sessionIDCol       = "session_id"
	stepCol            = "step"
	namespaceCol       = "namespace"
	rootCol            = "data_model_root"
	bootstrapCol       = "bootstrap"
	rotationSentCol    = "rotation_sent"
	pendingIDCol       = "pending_id"
	pendingMethodCol   = "pending_method"
	pendingTaskCol     = "pending_task_id"
	taskStepCol        = "task_step"
	requestSeqCol      = "request_seq"
	createdCol         = "created_sec"
	expiresCol         = "expires_sec"

	tasksTable       = "acs_tasks"
	tasksDeviceIdx   = "acs_tasks_device_idx"
	tasksStatusIdx   = "acs_tasks_status_idx"
	taskIDCol        = "task_id"
	typeCol          = "type"
	argsCol          = "args"
	statusCol        = "status"
	attemptsCol      = "attempts"
	maxAttemptsCol   = "max_attempts"
	faultCodeCol     = "fault_code"
	faultStringCol   = "fault_string"
	resultCol        = "result"
	seqCol           = "seq"
	deadlineCol      = "deadline_sec"
	deviceStateTable = "acs_device_state"
	modelJSONCol     = "model"

	secretsTable   = "acs_secrets"
	nameCol        = "name"
	valueCol       = "value"
	informRateTbl  = "acs_inform_rate"
	windowEndCol   = "window_end_sec"
	informCountCol = "inform_count"
)

var deviceCols = []string{
	deviceIDCol, networkIDCol, ouiCol, productClassCol, serialNumberCol, modelCol,
	firmwareCol, handlerCol, connReqURLCol, firstSeenCol, lastSeenCol, intervalCol, eventsCol,
}

var credentialsCols = []string{
	deviceIDCol, acsUsernameCol, acsPasswordHashCol, pendingHashCol, connReqUsernameCol, connReqPasswordCol, updatedCol,
}

type sqlACSStorage struct {
	db      *sql.DB
	builder sqorc.StatementBuilder
	sealer  *Sealer
	// sqlite has no JSONB, so acs_parameters is TEXT there.
	sqlite bool
}

// Option configures the SQL storage.
type Option func(*sqlACSStorage)

// WithSealer encrypts the ConnectionRequest password at rest. Without it the
// storage refuses to store or read one.
func WithSealer(s *Sealer) Option {
	return func(st *sqlACSStorage) { st.sealer = s }
}

// NewSQLACSStorage returns an ACSStorage backed by the Orc8r SQL database.
func NewSQLACSStorage(db *sql.DB, builder sqorc.StatementBuilder, opts ...Option) ACSStorage {
	// The driver type is the only dialect hint: tests run SQLite behind the
	// Postgres statement builder.
	s := &sqlACSStorage{db: db, builder: builder, sqlite: strings.Contains(fmt.Sprintf("%T", db.Driver()), "sqlite")}
	for _, o := range opts {
		o(s)
	}
	return s
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
			Column(intervalCol).Type(sqorc.ColumnTypeBigInt).NotNull().Default(0).EndColumn().
			Column(eventsCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
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
			Column(pendingHashCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
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
			Column(namespaceCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(rootCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(handlerCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(bootstrapCol).Type(sqorc.ColumnTypeBool).NotNull().Default("FALSE").EndColumn().
			Column(rotationSentCol).Type(sqorc.ColumnTypeBool).NotNull().Default("FALSE").EndColumn().
			Column(pendingIDCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(pendingMethodCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(pendingTaskCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(taskStepCol).Type(sqorc.ColumnTypeInt).NotNull().Default(0).EndColumn().
			Column(requestSeqCol).Type(sqorc.ColumnTypeInt).NotNull().Default(0).EndColumn().
			Column(createdCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Column(expiresCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			ForeignKey(devicesTable, map[string]string{deviceIDCol: deviceIDCol}, sqorc.ColumnOnDeleteCascade).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", sessionsTable, err)
		}

		_, err = s.builder.CreateTable(tasksTable).
			IfNotExists().
			Column(taskIDCol).Type(sqorc.ColumnTypeText).PrimaryKey().EndColumn().
			Column(deviceIDCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(networkIDCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(typeCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(argsCol).Type(sqorc.ColumnTypeText).NotNull().Default("'{}'").EndColumn().
			Column(statusCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(attemptsCol).Type(sqorc.ColumnTypeInt).NotNull().Default(0).EndColumn().
			Column(maxAttemptsCol).Type(sqorc.ColumnTypeInt).NotNull().EndColumn().
			Column(sessionIDCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(faultCodeCol).Type(sqorc.ColumnTypeInt).NotNull().Default(0).EndColumn().
			Column(faultStringCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(resultCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(seqCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Column(createdCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Column(updatedCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Column(deadlineCol).Type(sqorc.ColumnTypeBigInt).NotNull().Default(0).EndColumn().
			ForeignKey(devicesTable, map[string]string{deviceIDCol: deviceIDCol}, sqorc.ColumnOnDeleteCascade).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", tasksTable, err)
		}

		_, err = s.builder.CreateTable(deviceStateTable).
			IfNotExists().
			Column(deviceIDCol).Type(sqorc.ColumnTypeText).PrimaryKey().EndColumn().
			Column(handlerCol).Type(sqorc.ColumnTypeText).NotNull().Default("''").EndColumn().
			Column(modelJSONCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			Column(updatedCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			ForeignKey(devicesTable, map[string]string{deviceIDCol: deviceIDCol}, sqorc.ColumnOnDeleteCascade).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", deviceStateTable, err)
		}

		_, err = s.builder.CreateTable(secretsTable).
			IfNotExists().
			Column(nameCol).Type(sqorc.ColumnTypeText).PrimaryKey().EndColumn().
			Column(valueCol).Type(sqorc.ColumnTypeText).NotNull().EndColumn().
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", secretsTable, err)
		}

		_, err = s.builder.CreateTable(informRateTbl).
			IfNotExists().
			Column(deviceIDCol).Type(sqorc.ColumnTypeText).PrimaryKey().EndColumn().
			Column(windowEndCol).Type(sqorc.ColumnTypeBigInt).NotNull().EndColumn().
			Column(informCountCol).Type(sqorc.ColumnTypeInt).NotNull().EndColumn().
			ForeignKey(devicesTable, map[string]string{deviceIDCol: deviceIDCol}, sqorc.ColumnOnDeleteCascade).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create %s table: %w", informRateTbl, err)
		}

		if _, err = tx.Exec(s.parametersDDL()); err != nil {
			return nil, fmt.Errorf("create %s table: %w", parametersTable, err)
		}

		indexes := []struct{ name, table, col string }{
			{sessionsDeviceIdx, sessionsTable, deviceIDCol},
			{sessionsExpiresIdx, sessionsTable, expiresCol},
			{tasksDeviceIdx, tasksTable, deviceIDCol},
			{tasksStatusIdx, tasksTable, statusCol},
		}
		for _, idx := range indexes {
			_, err = s.builder.CreateIndex(idx.name).
				IfNotExists().
				On(idx.table).
				Columns(idx.col).
				RunWith(tx).
				Exec()
			if err != nil {
				return nil, fmt.Errorf("create %s index: %w", idx.name, err)
			}
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, &sql.TxOptions{Isolation: sql.LevelSerializable}, nil, txFn)
	return err
}

func (s *sqlACSStorage) UpsertDevice(d *Device) error {
	events, err := marshalEvents(d.LastInformEvents)
	if err != nil {
		return err
	}
	txFn := func(tx *sql.Tx) (interface{}, error) {
		// network_id is left out of the update on purpose: which network owns
		// a device is decided by a claim, never by what the device reports.
		_, err := s.builder.Insert(devicesTable).
			Columns(deviceCols...).
			Values(d.DeviceID, toNullString(d.NetworkID), d.OUI, d.ProductClass, d.SerialNumber, d.Model,
				d.Firmware, d.Handler, d.ConnectionRequestURL, d.FirstSeenSec, d.LastSeenSec, d.InformIntervalSec, events).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: modelCol, Value: d.Model},
					{Column: firmwareCol, Value: d.Firmware},
					{Column: handlerCol, Value: d.Handler},
					{Column: connReqURLCol, Value: d.ConnectionRequestURL},
					{Column: lastSeenCol, Value: d.LastSeenSec},
					{Column: intervalCol, Value: d.InformIntervalSec},
					{Column: eventsCol, Value: events},
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
	_, err = sqorc.ExecInTx(s.db, nil, nil, txFn)
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
	return s.FindDevices(DeviceFilter{NetworkID: networkID})
}

func (s *sqlACSStorage) FindDevices(f DeviceFilter) ([]*Device, error) {
	var where sq.And
	switch {
	case f.Unclaimed:
		where = append(where, sq.Eq{networkIDCol: nil})
	case f.NetworkID != "":
		where = append(where, sq.Eq{networkIDCol: f.NetworkID})
	default:
		return nil, errors.New("find devices: network ID or unclaimed required")
	}
	if f.Model != "" {
		where = append(where, sq.Eq{modelCol: f.Model})
	}
	if f.Online != nil {
		// Same rule as OnlinePolicy.Online.
		cond := fmt.Sprintf("(? - %s) * 1000 <= ? * (CASE WHEN %s > 0 THEN %s ELSE ? END)", lastSeenCol, intervalCol, intervalCol)
		if !*f.Online {
			cond = "NOT (" + cond + ")"
		}
		where = append(where, sq.Expr(cond, f.NowSec, f.Policy.multipleMilli(), f.Policy.defaultInterval()))
	}
	return s.selectDevices(where)
}

func (s *sqlACSStorage) ClaimDevice(deviceID, networkID string) error {
	if networkID == "" {
		return errors.New("claim device: network ID required")
	}
	txFn := func(tx *sql.Tx) (interface{}, error) {
		res, err := s.builder.Update(devicesTable).
			Set(networkIDCol, networkID).
			Where(sq.And{sq.Eq{deviceIDCol: deviceID}, sq.Eq{networkIDCol: nil}}).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("claim device %s: %w", deviceID, err)
		}
		if n, err := res.RowsAffected(); err != nil || n == 1 {
			return nil, err
		}
		var exists int
		err = s.builder.Select("1").From(devicesTable).Where(sq.Eq{deviceIDCol: deviceID}).
			RunWith(tx).QueryRow().Scan(&exists)
		if err == sql.ErrNoRows {
			return nil, ErrDeviceNotFound
		}
		if err != nil {
			return nil, fmt.Errorf("claim device %s: %w", deviceID, err)
		}
		return nil, ErrDeviceClaimed
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func marshalEvents(events []string) (string, error) {
	if len(events) == 0 {
		return "", nil
	}
	b, err := json.Marshal(events)
	return string(b), err
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
			var events string
			err = rows.Scan(&d.DeviceID, &networkID, &d.OUI, &d.ProductClass, &d.SerialNumber, &d.Model,
				&d.Firmware, &d.Handler, &d.ConnectionRequestURL, &d.FirstSeenSec, &d.LastSeenSec, &d.InformIntervalSec, &events)
			if err != nil {
				return nil, fmt.Errorf("scan device: %w", err)
			}
			d.NetworkID = networkID.String
			if events != "" {
				if err := json.Unmarshal([]byte(events), &d.LastInformEvents); err != nil {
					return nil, fmt.Errorf("device %s events: %w", d.DeviceID, err)
				}
			}
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
	sealedCRPassword, err := sealSecret(s.sealer, c.ConnReqPassword, c.DeviceID)
	if err != nil {
		return fmt.Errorf("put credentials for %s: %w", c.DeviceID, err)
	}
	txFn := func(tx *sql.Tx) (interface{}, error) {
		_, err := s.builder.Insert(credentialsTable).
			Columns(credentialsCols...).
			Values(c.DeviceID, c.ACSUsername, c.ACSPasswordHash, c.PendingPasswordHash, c.ConnReqUsername, sealedCRPassword, c.UpdatedSec).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: acsUsernameCol, Value: c.ACSUsername},
					{Column: acsPasswordHashCol, Value: c.ACSPasswordHash},
					{Column: pendingHashCol, Value: c.PendingPasswordHash},
					{Column: connReqUsernameCol, Value: c.ConnReqUsername},
					{Column: connReqPasswordCol, Value: sealedCRPassword},
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
	_, err = sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) GetCredentials(deviceID string) (*Credentials, error) {
	return s.getCredentials(sq.Eq{deviceIDCol: deviceID})
}

func (s *sqlACSStorage) GetCredentialsByUsername(acsUsername string) (*Credentials, error) {
	return s.getCredentials(sq.Eq{acsUsernameCol: acsUsername})
}

func (s *sqlACSStorage) getCredentials(where sq.Sqlizer) (*Credentials, error) {
	c := &Credentials{}
	err := s.builder.Select(credentialsCols...).
		From(credentialsTable).
		Where(where).
		RunWith(s.db).
		QueryRowContext(context.Background()).
		Scan(&c.DeviceID, &c.ACSUsername, &c.ACSPasswordHash, &c.PendingPasswordHash, &c.ConnReqUsername, &c.ConnReqPassword, &c.UpdatedSec)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get credentials: %w", err)
	}
	if c.ConnReqPassword, err = openSecret(s.sealer, c.ConnReqPassword, c.DeviceID); err != nil {
		return nil, fmt.Errorf("get credentials of %s: %w", c.DeviceID, err)
	}
	return c, nil
}

func (s *sqlACSStorage) DeleteCredentials(deviceID string) error {
	_, err := s.builder.Delete(credentialsTable).
		Where(sq.Eq{deviceIDCol: deviceID}).
		RunWith(s.db).
		Exec()
	if err != nil {
		return fmt.Errorf("delete credentials of %s: %w", deviceID, err)
	}
	return nil
}

func toNullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
