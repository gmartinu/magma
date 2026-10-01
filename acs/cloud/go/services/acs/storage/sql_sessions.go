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

var sessionCols = []string{
	sessionIDCol, deviceIDCol, stepCol, namespaceCol, rootCol, handlerCol, bootstrapCol, rotationSentCol,
	pendingIDCol, pendingMethodCol, pendingTaskCol, taskStepCol, requestSeqCol, createdCol, expiresCol,
}

func (s *sqlACSStorage) PutSession(session *Session) error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		_, err := s.builder.Insert(sessionsTable).
			Columns(sessionCols...).
			Values(session.SessionID, session.DeviceID, session.Step, session.Namespace, session.Root, session.Handler,
				session.Bootstrap, session.RotationSent, session.PendingID, session.PendingMethod, session.PendingTaskID,
				session.TaskStep, session.RequestSeq, session.CreatedSec, session.ExpiresSec).
			OnConflict(
				[]sqorc.UpsertValue{
					{Column: stepCol, Value: session.Step},
					{Column: namespaceCol, Value: session.Namespace},
					{Column: rootCol, Value: session.Root},
					{Column: handlerCol, Value: session.Handler},
					{Column: bootstrapCol, Value: session.Bootstrap},
					{Column: rotationSentCol, Value: session.RotationSent},
					{Column: pendingIDCol, Value: session.PendingID},
					{Column: pendingMethodCol, Value: session.PendingMethod},
					{Column: pendingTaskCol, Value: session.PendingTaskID},
					{Column: taskStepCol, Value: session.TaskStep},
					{Column: requestSeqCol, Value: session.RequestSeq},
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
	return s.getSession(sq.Eq{sessionIDCol: sessionID})
}

func (s *sqlACSStorage) GetDeviceSession(deviceID string) (*Session, error) {
	return s.getSession(sq.Eq{deviceIDCol: deviceID})
}

func (s *sqlACSStorage) getSession(where sq.Sqlizer) (*Session, error) {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		rows, err := s.builder.Select(sessionCols...).
			From(sessionsTable).
			Where(sq.And{where, sq.Gt{expiresCol: clock.Now().Unix()}}).
			OrderBy(createdCol+" DESC", sessionIDCol).
			Limit(1).
			RunWith(tx).
			Query()
		if err != nil {
			return nil, fmt.Errorf("select session: %w", err)
		}
		defer sqorc.CloseRowsLogOnError(rows, "getSession")
		if !rows.Next() {
			return (*Session)(nil), rows.Err()
		}
		ss := &Session{}
		err = rows.Scan(&ss.SessionID, &ss.DeviceID, &ss.Step, &ss.Namespace, &ss.Root, &ss.Handler, &ss.Bootstrap,
			&ss.RotationSent, &ss.PendingID, &ss.PendingMethod, &ss.PendingTaskID, &ss.TaskStep, &ss.RequestSeq,
			&ss.CreatedSec, &ss.ExpiresSec)
		if err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		return ss, nil
	}
	ret, err := sqorc.ExecInTx(s.db, &sql.TxOptions{ReadOnly: true}, nil, txFn)
	if err != nil {
		return nil, err
	}
	return ret.(*Session), nil
}

func (s *sqlACSStorage) EndSession(sessionID string, reason string) error {
	return s.endSessions(sq.Eq{sessionIDCol: sessionID}, reason)
}

func (s *sqlACSStorage) EndDeviceSessions(deviceID string, reason string) error {
	return s.endSessions(sq.Eq{deviceIDCol: deviceID}, reason)
}

func (s *sqlACSStorage) endSessions(where sq.Sqlizer, reason string) error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		_, err := s.builder.Delete(sessionsTable).Where(where).RunWith(tx).Exec()
		if err != nil {
			return nil, fmt.Errorf("delete sessions: %w", err)
		}
		_, err = s.requeueOrphans(tx, reason)
		return nil, err
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) ReapExpired() (ReapResult, error) {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		res := ReapResult{}
		now := clock.Now().Unix()
		r, err := s.builder.Delete(sessionsTable).
			Where(sq.LtOrEq{expiresCol: now}).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("delete expired sessions: %w", err)
		}
		res.Sessions = rowsAffected(r)

		res.RequeuedTasks, err = s.requeueOrphans(tx, "session timed out")
		if err != nil {
			return nil, err
		}

		r, err = s.builder.Update(tasksTable).
			Set(statusCol, TaskExpired).
			Set(updatedCol, now).
			Where(sq.And{
				sq.Eq{statusCol: TaskPending},
				sq.Gt{deadlineCol: 0},
				sq.LtOrEq{deadlineCol: now},
			}).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("expire tasks: %w", err)
		}
		res.ExpiredTasks = rowsAffected(r)
		return res, nil
	}
	ret, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	if err != nil {
		return ReapResult{}, err
	}
	return ret.(ReapResult), nil
}

// requeueOrphans treats a task left in progress by a session that no longer
// exists as a retryable fault.
func (s *sqlACSStorage) requeueOrphans(tx *sql.Tx, reason string) (int, error) {
	now := clock.Now().Unix()
	orphan := sq.And{
		sq.Eq{statusCol: TaskInProgress},
		sq.Expr(fmt.Sprintf("%s NOT IN (SELECT %s FROM %s)", sessionIDCol, sessionIDCol, sessionsTable)),
	}
	retryable := sq.And{
		sq.Expr(fmt.Sprintf("%s < %s", attemptsCol, maxAttemptsCol)),
		sq.Or{sq.Eq{deadlineCol: 0}, sq.Gt{deadlineCol: now}},
	}
	total := 0
	for _, step := range []struct {
		where  sq.Sqlizer
		status string
	}{
		{sq.And{orphan, retryable}, TaskPending},
		{orphan, TaskFailed},
	} {
		r, err := s.builder.Update(tasksTable).
			Set(statusCol, step.status).
			Set(sessionIDCol, "").
			Set(faultCodeCol, 0).
			Set(faultStringCol, reason).
			Set(updatedCol, now).
			Where(step.where).
			RunWith(tx).
			Exec()
		if err != nil {
			return 0, fmt.Errorf("requeue orphaned tasks: %w", err)
		}
		total += rowsAffected(r)
	}
	return total, nil
}

func rowsAffected(r sql.Result) int {
	n, err := r.RowsAffected()
	if err != nil {
		return 0
	}
	return int(n)
}
