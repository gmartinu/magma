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

package storage_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/sqorc"
)

func TestSQLACSStorage_SQLite(t *testing.T) {
	db, err := sqorc.Open("sqlite3", ":memory:?_foreign_keys=1")
	require.NoError(t, err)
	runStorageTests(t, storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder()))
}

// runStorageTests is shared with the Postgres integration test so both
// dialects are held to the same behavior.
func runStorageTests(t *testing.T, store storage.ACSStorage) {
	require.NoError(t, store.Init())
	// Init runs on every service start, so it must be idempotent.
	require.NoError(t, store.Init())

	clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	defer clock.UnfreezeClock(t)

	t.Run("devices", func(t *testing.T) {
		devices, err := store.ListDevices("n1")
		assert.NoError(t, err)
		assert.Empty(t, devices)

		got, err := store.GetDevice("missing")
		assert.NoError(t, err)
		assert.Nil(t, got)

		claimed := &storage.Device{
			DeviceID:     "00259E-Titan4000-SN2",
			NetworkID:    "n1",
			OUI:          "00259E",
			ProductClass: "Titan4000",
			SerialNumber: "SN2",
			Model:        "Titan 4000",
			Firmware:     "1.0.0",
			Handler:      "titan4000",
			FirstSeenSec: 100,
			LastSeenSec:  100,
		}
		unclaimed := &storage.Device{
			DeviceID:     "00259E-Titan4000-SN1",
			OUI:          "00259E",
			ProductClass: "Titan4000",
			SerialNumber: "SN1",
			FirstSeenSec: 200,
			LastSeenSec:  200,
		}
		other := &storage.Device{
			DeviceID:     "00259E-Titan4000-SN0",
			NetworkID:    "n2",
			OUI:          "00259E",
			ProductClass: "Titan4000",
			SerialNumber: "SN0",
			FirstSeenSec: 300,
			LastSeenSec:  300,
		}
		for _, d := range []*storage.Device{claimed, unclaimed, other} {
			require.NoError(t, store.UpsertDevice(d))
		}

		devices, err = store.ListDevices("n1")
		assert.NoError(t, err)
		assert.Equal(t, []*storage.Device{claimed}, devices)

		got, err = store.GetDevice(unclaimed.DeviceID)
		assert.NoError(t, err)
		assert.Equal(t, unclaimed, got)

		// A later Inform refreshes what the device reports, but neither its
		// network nor its first-seen time.
		refreshed := *claimed
		refreshed.NetworkID = "n2"
		refreshed.Firmware = "1.1.0"
		refreshed.ConnectionRequestURL = "http://10.0.0.2:7547/cr"
		refreshed.FirstSeenSec = 999
		refreshed.LastSeenSec = 500
		require.NoError(t, store.UpsertDevice(&refreshed))

		expected := *claimed
		expected.Firmware = "1.1.0"
		expected.ConnectionRequestURL = "http://10.0.0.2:7547/cr"
		expected.LastSeenSec = 500
		got, err = store.GetDevice(claimed.DeviceID)
		assert.NoError(t, err)
		assert.Equal(t, &expected, got)
	})

	t.Run("credentials", func(t *testing.T) {
		got, err := store.GetCredentialsByUsername("nobody")
		assert.NoError(t, err)
		assert.Nil(t, got)

		creds := &storage.Credentials{
			DeviceID:        "00259E-Titan4000-SN2",
			ACSUsername:     "cpe-sn2",
			ACSPasswordHash: "hash1",
			ConnReqUsername: "cr-user",
			ConnReqPassword: "cr-pass",
			UpdatedSec:      100,
		}
		require.NoError(t, store.PutCredentials(creds))
		got, err = store.GetCredentialsByUsername("cpe-sn2")
		assert.NoError(t, err)
		assert.Equal(t, creds, got)

		got, err = store.GetCredentials(creds.DeviceID)
		assert.NoError(t, err)
		assert.Equal(t, creds, got)

		rotated := *creds
		rotated.ACSPasswordHash = "hash2"
		rotated.PendingPasswordHash = "hash3"
		rotated.UpdatedSec = 200
		require.NoError(t, store.PutCredentials(&rotated))
		got, err = store.GetCredentialsByUsername("cpe-sn2")
		assert.NoError(t, err)
		assert.Equal(t, &rotated, got)

		// Usernames identify the CPE on every request, so two devices can
		// never share one.
		dup := *creds
		dup.DeviceID = "00259E-Titan4000-SN1"
		assert.Error(t, store.PutCredentials(&dup))

		// Credentials only exist for known devices.
		orphan := *creds
		orphan.DeviceID = "unknown"
		orphan.ACSUsername = "cpe-unknown"
		assert.Error(t, store.PutCredentials(&orphan))

		require.NoError(t, store.DeleteCredentials(creds.DeviceID))
		got, err = store.GetCredentials(creds.DeviceID)
		assert.NoError(t, err)
		assert.Nil(t, got)
		require.NoError(t, store.PutCredentials(&rotated))
	})

	t.Run("sessions", func(t *testing.T) {
		got, err := store.GetSession("missing")
		assert.NoError(t, err)
		assert.Nil(t, got)

		live := &storage.Session{
			SessionID: "cookie-live", DeviceID: "00259E-Titan4000-SN2", Step: storage.SessionCPERequests,
			Namespace: "urn:dslforum-org:cwmp-1-2", Root: "Device.", Handler: "generic", Bootstrap: true,
			CreatedSec: 990, ExpiresSec: 1030,
		}
		older := &storage.Session{SessionID: "cookie-older", DeviceID: "00259E-Titan4000-SN2", Step: storage.SessionCPERequests, CreatedSec: 980, ExpiresSec: 1030}
		expired := &storage.Session{SessionID: "cookie-expired", DeviceID: "00259E-Titan4000-SN1", Step: storage.SessionCPERequests, CreatedSec: 900, ExpiresSec: 1000}
		for _, ss := range []*storage.Session{live, older, expired} {
			require.NoError(t, store.PutSession(ss))
		}

		got, err = store.GetSession(live.SessionID)
		assert.NoError(t, err)
		assert.Equal(t, live, got)
		got, err = store.GetSession(expired.SessionID)
		assert.NoError(t, err)
		assert.Nil(t, got)
		got, err = store.GetDeviceSession(live.DeviceID)
		assert.NoError(t, err)
		assert.Equal(t, live, got)
		got, err = store.GetDeviceSession(expired.DeviceID)
		assert.NoError(t, err)
		assert.Nil(t, got)

		advanced := *live
		advanced.Step = storage.SessionACSRequests
		advanced.RotationSent = true
		advanced.PendingID, advanced.PendingMethod, advanced.PendingTaskID = "id-3", "Reboot", "t1"
		advanced.TaskStep, advanced.RequestSeq = 2, 3
		advanced.ExpiresSec = 1060
		require.NoError(t, store.PutSession(&advanced))
		got, err = store.GetSession(live.SessionID)
		assert.NoError(t, err)
		assert.Equal(t, &advanced, got)

		require.NoError(t, store.EndSession(older.SessionID, "done"))
		got, err = store.GetSession(older.SessionID)
		assert.NoError(t, err)
		assert.Nil(t, got)

		res, err := store.ReapExpired()
		require.NoError(t, err)
		assert.Equal(t, storage.ReapResult{Sessions: 1}, res)

		require.NoError(t, store.EndDeviceSessions(live.DeviceID, "new session"))
		got, err = store.GetSession(live.SessionID)
		assert.NoError(t, err)
		assert.Nil(t, got)
	})

	t.Run("tasks", func(t *testing.T) {
		const dev = "00259E-Titan4000-SN2"
		tasks, err := store.ListTasks(dev)
		require.NoError(t, err)
		assert.Empty(t, tasks)
		got, err := store.GetTask("missing")
		assert.NoError(t, err)
		assert.Nil(t, got)
		assert.ErrorIs(t, store.CompleteTask("missing", ""), storage.ErrTaskNotFound)
		assert.ErrorIs(t, store.FailTask("missing", 1, "", true), storage.ErrTaskNotFound)

		newTask := func(id string, maxAttempts int, deadline int64) *storage.Task {
			return &storage.Task{
				TaskID: id, DeviceID: dev, NetworkID: "n1", Type: "reboot", Args: "{}", Status: storage.TaskPending,
				MaxAttempts: maxAttempts, CreatedSec: 1000, UpdatedSec: 1000, DeadlineSec: deadline,
			}
		}
		// Created in the same clock tick, in an order their IDs do not sort by.
		for _, task := range []*storage.Task{newTask("z", 2, 0), newTask("a", 1, 0), newTask("m", 1, 1100), newTask("late", 1, 1050)} {
			require.NoError(t, store.CreateTask(task))
		}
		assert.Error(t, store.CreateTask(newTask("z", 1, 0)))
		orphan := newTask("orphan", 1, 0)
		orphan.DeviceID = "unknown"
		assert.Error(t, store.CreateTask(orphan))

		tasks, err = store.ListTasks(dev)
		require.NoError(t, err)
		require.Len(t, tasks, 4)
		assert.Equal(t, []string{"z", "a", "m", "late"}, []string{tasks[0].TaskID, tasks[1].TaskID, tasks[2].TaskID, tasks[3].TaskID})
		assert.Equal(t, newTask("z", 2, 0), tasks[0])

		// Claim in creation order; a claimed task is not claimed again.
		claimed, err := store.ClaimNextTask(dev, "s1")
		require.NoError(t, err)
		assert.Equal(t, "z", claimed.TaskID)
		assert.Equal(t, storage.TaskInProgress, claimed.Status)
		assert.Equal(t, 1, claimed.Attempts)
		assert.Equal(t, "s1", claimed.SessionID)
		got, err = store.GetTask("z")
		require.NoError(t, err)
		assert.Equal(t, claimed, got)

		// A retryable fault with attempts left requeues the task, with the
		// fault written back.
		require.NoError(t, store.FailTask("z", 9002, "internal error", true))
		got, _ = store.GetTask("z")
		assert.Equal(t, storage.TaskPending, got.Status)
		assert.Equal(t, 9002, got.FaultCode)
		assert.Equal(t, "s1", got.SessionID)

		// Not retried in the session that faulted, but in the next one.
		claimed, err = store.ClaimNextTask(dev, "s1")
		require.NoError(t, err)
		assert.Equal(t, "a", claimed.TaskID)
		require.NoError(t, store.FailTask("a", 9002, "internal error", true))
		got, _ = store.GetTask("a")
		assert.Equal(t, storage.TaskFailed, got.Status, "a has one attempt")
		claimed, err = store.ClaimNextTask(dev, "s1b")
		require.NoError(t, err)
		assert.Equal(t, "z", claimed.TaskID)
		assert.Equal(t, 2, claimed.Attempts)
		// Out of attempts: retryable or not, the task fails.
		require.NoError(t, store.FailTask("z", 9002, "internal error", true))
		got, _ = store.GetTask("z")
		assert.Equal(t, storage.TaskFailed, got.Status)

		claimed, _ = store.ClaimNextTask(dev, "s1")
		assert.Equal(t, "m", claimed.TaskID)
		require.NoError(t, store.SaveTaskResult("m", `{"partial":true}`))
		got, _ = store.GetTask("m")
		assert.Equal(t, storage.TaskInProgress, got.Status)
		assert.Equal(t, `{"partial":true}`, got.Result)
		require.NoError(t, store.CompleteTask("m", `{"ok":true}`))
		got, _ = store.GetTask("m")
		assert.Equal(t, storage.TaskDone, got.Status)
		assert.Equal(t, `{"ok":true}`, got.Result)

		// Past its deadline a pending task is neither claimed nor kept.
		clock.SetAndFreezeClock(t, time.Unix(1050, 0))
		claimed, err = store.ClaimNextTask(dev, "s1")
		require.NoError(t, err)
		assert.Nil(t, claimed)
		res, err := store.ReapExpired()
		require.NoError(t, err)
		assert.Equal(t, storage.ReapResult{ExpiredTasks: 1}, res)
		got, _ = store.GetTask("late")
		assert.Equal(t, storage.TaskExpired, got.Status)

		require.NoError(t, store.CreateTask(newTask("perm", 3, 0)))
		claimed, _ = store.ClaimNextTask(dev, "s1")
		assert.Equal(t, "perm", claimed.TaskID)
		require.NoError(t, store.FailTask("perm", 9005, "invalid parameter name", false))
		got, _ = store.GetTask("perm")
		assert.Equal(t, storage.TaskFailed, got.Status)
		assert.Equal(t, "invalid parameter name", got.FaultString)
		assert.Equal(t, "", got.SessionID)

		// A task in progress in a session that ends is requeued; one out of
		// attempts fails.
		require.NoError(t, store.CreateTask(newTask("r1", 2, 0)))
		require.NoError(t, store.CreateTask(newTask("r2", 1, 0)))
		require.NoError(t, store.PutSession(&storage.Session{SessionID: "s2", DeviceID: dev, Step: storage.SessionACSRequests, CreatedSec: 1050, ExpiresSec: 1100}))
		claimed, _ = store.ClaimNextTask(dev, "s2")
		assert.Equal(t, "r1", claimed.TaskID)
		require.NoError(t, store.EndSession("s2", "CPE started a new session"))
		got, _ = store.GetTask("r1")
		assert.Equal(t, storage.TaskPending, got.Status)
		assert.Equal(t, "CPE started a new session", got.FaultString)

		require.NoError(t, store.PutSession(&storage.Session{SessionID: "s3", DeviceID: dev, Step: storage.SessionACSRequests, CreatedSec: 1050, ExpiresSec: 1060}))
		claimed, _ = store.ClaimNextTask(dev, "s3")
		assert.Equal(t, "r1", claimed.TaskID)
		claimed, _ = store.ClaimNextTask(dev, "s3")
		assert.Equal(t, "r2", claimed.TaskID)
		clock.SetAndFreezeClock(t, time.Unix(1070, 0))
		res, err = store.ReapExpired()
		require.NoError(t, err)
		assert.Equal(t, storage.ReapResult{Sessions: 1, RequeuedTasks: 2}, res)
		got, _ = store.GetTask("r1")
		assert.Equal(t, storage.TaskFailed, got.Status)
		assert.Equal(t, "session timed out", got.FaultString)
		got, _ = store.GetTask("r2")
		assert.Equal(t, storage.TaskFailed, got.Status)
		clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	})

	t.Run("device state", func(t *testing.T) {
		got, err := store.GetDeviceState("00259E-Titan4000-SN2")
		assert.NoError(t, err)
		assert.Nil(t, got)
		st := &storage.DeviceState{DeviceID: "00259E-Titan4000-SN2", Handler: "generic", Model: `{"root":"Device."}`, UpdatedSec: 1}
		require.NoError(t, store.PutDeviceState(st))
		st.Model, st.UpdatedSec = `{"root":"InternetGatewayDevice."}`, 2
		require.NoError(t, store.PutDeviceState(st))
		got, err = store.GetDeviceState(st.DeviceID)
		assert.NoError(t, err)
		assert.Equal(t, st, got)
	})

	t.Run("secrets", func(t *testing.T) {
		calls := 0
		gen := func(v string) func() (string, error) {
			return func() (string, error) { calls++; return v, nil }
		}
		v, err := store.GetOrCreateSecret("nonce", gen("first"))
		require.NoError(t, err)
		assert.Equal(t, "first", v)
		v, err = store.GetOrCreateSecret("nonce", gen("second"))
		require.NoError(t, err)
		assert.Equal(t, "first", v)
		assert.Equal(t, 1, calls)
	})

	t.Run("inform rate", func(t *testing.T) {
		const dev = "00259E-Titan4000-SN1"
		for want := 1; want <= 3; want++ {
			n, end, err := store.CountInform(dev, 60)
			require.NoError(t, err)
			assert.Equal(t, want, n)
			assert.Equal(t, int64(1060), end)
		}
		clock.SetAndFreezeClock(t, time.Unix(1060, 0))
		n, end, err := store.CountInform(dev, 60)
		require.NoError(t, err)
		assert.Equal(t, 1, n)
		assert.Equal(t, int64(1120), end)
		clock.SetAndFreezeClock(t, time.Unix(1000, 0))
	})
}

func testKey() []byte {
	key := make([]byte, storage.EncryptionKeySize)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}
