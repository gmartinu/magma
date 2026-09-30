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
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"

	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/sqorc"
)

// ErrTaskNotFound is returned when updating a task that does not exist.
var ErrTaskNotFound = errors.New("task not found")

var taskCols = []string{
	taskIDCol, deviceIDCol, networkIDCol, typeCol, argsCol, statusCol, attemptsCol, maxAttemptsCol, sessionIDCol,
	faultCodeCol, faultStringCol, resultCol, createdCol, updatedCol, deadlineCol,
}

func (s *sqlACSStorage) CreateTask(t *Task) error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		// seq orders the tasks of a device even when several are created in
		// the same clock tick.
		var maxSeq sql.NullInt64
		err := s.builder.Select("MAX(" + seqCol + ")").
			From(tasksTable).
			Where(sq.Eq{deviceIDCol: t.DeviceID}).
			RunWith(tx).
			QueryRow().
			Scan(&maxSeq)
		if err != nil {
			return nil, fmt.Errorf("select task seq: %w", err)
		}
		seq := clock.Now().UnixNano()
		if maxSeq.Valid && seq <= maxSeq.Int64 {
			seq = maxSeq.Int64 + 1
		}
		_, err = s.builder.Insert(tasksTable).
			Columns(append(taskCols, seqCol)...).
			Values(t.TaskID, t.DeviceID, t.NetworkID, t.Type, t.Args, t.Status, t.Attempts, t.MaxAttempts, t.SessionID,
				t.FaultCode, t.FaultString, t.Result, t.CreatedSec, t.UpdatedSec, t.DeadlineSec, seq).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("create task for %s: %w", t.DeviceID, err)
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) GetTask(taskID string) (*Task, error) {
	tasks, err := s.selectTasks(sq.Eq{taskIDCol: taskID})
	if err != nil || len(tasks) == 0 {
		return nil, err
	}
	return tasks[0], nil
}

func (s *sqlACSStorage) ListTasks(deviceID string) ([]*Task, error) {
	return s.selectTasks(sq.Eq{deviceIDCol: deviceID})
}

func (s *sqlACSStorage) selectTasks(where sq.Sqlizer) ([]*Task, error) {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		return s.queryTasks(tx, s.builder.Select(taskCols...).From(tasksTable).Where(where).OrderBy(seqCol))
	}
	ret, err := sqorc.ExecInTx(s.db, &sql.TxOptions{ReadOnly: true}, nil, txFn)
	if err != nil {
		return nil, err
	}
	return ret.([]*Task), nil
}

func (s *sqlACSStorage) queryTasks(tx *sql.Tx, q sq.SelectBuilder) ([]*Task, error) {
	rows, err := q.RunWith(tx).Query()
	if err != nil {
		return nil, fmt.Errorf("select tasks: %w", err)
	}
	defer sqorc.CloseRowsLogOnError(rows, "queryTasks")
	tasks := []*Task{}
	for rows.Next() {
		t := &Task{}
		err = rows.Scan(&t.TaskID, &t.DeviceID, &t.NetworkID, &t.Type, &t.Args, &t.Status, &t.Attempts, &t.MaxAttempts,
			&t.SessionID, &t.FaultCode, &t.FaultString, &t.Result, &t.CreatedSec, &t.UpdatedSec, &t.DeadlineSec)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (s *sqlACSStorage) ClaimNextTask(deviceID, sessionID string) (*Task, error) {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		now := clock.Now().Unix()
		q := s.builder.Select(taskCols...).
			From(tasksTable).
			Where(sq.And{
				sq.Eq{deviceIDCol: deviceID, statusCol: TaskPending},
				sq.Or{sq.Eq{deadlineCol: 0}, sq.Gt{deadlineCol: now}},
			}).
			OrderBy(seqCol).
			Limit(1).
			Suffix(sqorc.GetSqlLocker().WithLock())
		tasks, err := s.queryTasks(tx, q)
		if err != nil || len(tasks) == 0 {
			return (*Task)(nil), err
		}
		t := tasks[0]
		t.Status, t.SessionID, t.Attempts, t.UpdatedSec = TaskInProgress, sessionID, t.Attempts+1, now
		_, err = s.builder.Update(tasksTable).
			Set(statusCol, t.Status).
			Set(sessionIDCol, t.SessionID).
			Set(attemptsCol, t.Attempts).
			Set(updatedCol, now).
			Where(sq.Eq{taskIDCol: t.TaskID}).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("claim task %s: %w", t.TaskID, err)
		}
		return t, nil
	}
	ret, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	if err != nil {
		return nil, err
	}
	return ret.(*Task), nil
}

func (s *sqlACSStorage) SaveTaskResult(taskID, result string) error {
	return s.updateTask(taskID, map[string]interface{}{resultCol: result})
}

func (s *sqlACSStorage) CompleteTask(taskID, result string) error {
	return s.updateTask(taskID, map[string]interface{}{
		statusCol: TaskDone, resultCol: result, sessionIDCol: "", faultCodeCol: 0, faultStringCol: "",
	})
}

func (s *sqlACSStorage) FailTask(taskID string, faultCode int, faultString string, retryable bool) error {
	txFn := func(tx *sql.Tx) (interface{}, error) {
		q := s.builder.Select(taskCols...).
			From(tasksTable).
			Where(sq.Eq{taskIDCol: taskID}).
			Suffix(sqorc.GetSqlLocker().WithLock())
		tasks, err := s.queryTasks(tx, q)
		if err != nil {
			return nil, err
		}
		if len(tasks) == 0 {
			return nil, ErrTaskNotFound
		}
		t, now := tasks[0], clock.Now().Unix()
		status := TaskFailed
		if retryable && t.Attempts < t.MaxAttempts && (t.DeadlineSec == 0 || t.DeadlineSec > now) {
			status = TaskPending
		}
		_, err = s.builder.Update(tasksTable).
			Set(statusCol, status).
			Set(sessionIDCol, "").
			Set(faultCodeCol, faultCode).
			Set(faultStringCol, faultString).
			Set(updatedCol, now).
			Where(sq.Eq{taskIDCol: taskID}).
			RunWith(tx).
			Exec()
		if err != nil {
			return nil, fmt.Errorf("fail task %s: %w", taskID, err)
		}
		return nil, nil
	}
	_, err := sqorc.ExecInTx(s.db, nil, nil, txFn)
	return err
}

func (s *sqlACSStorage) updateTask(taskID string, set map[string]interface{}) error {
	set[updatedCol] = clock.Now().Unix()
	r, err := s.builder.Update(tasksTable).
		SetMap(set).
		Where(sq.Eq{taskIDCol: taskID}).
		RunWith(s.db).
		Exec()
	if err != nil {
		return fmt.Errorf("update task %s: %w", taskID, err)
	}
	if rowsAffected(r) == 0 {
		return ErrTaskNotFound
	}
	return nil
}
