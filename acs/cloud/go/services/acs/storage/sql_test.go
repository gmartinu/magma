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

		rotated := *creds
		rotated.ACSPasswordHash = "hash2"
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
	})

	t.Run("sessions", func(t *testing.T) {
		got, err := store.GetSession("missing")
		assert.NoError(t, err)
		assert.Nil(t, got)

		live := &storage.Session{SessionID: "cookie-live", DeviceID: "00259E-Titan4000-SN2", Step: "inform", CreatedSec: 990, ExpiresSec: 1030}
		expired := &storage.Session{SessionID: "cookie-expired", DeviceID: "00259E-Titan4000-SN1", Step: "inform", CreatedSec: 900, ExpiresSec: 1000}
		require.NoError(t, store.PutSession(live))
		require.NoError(t, store.PutSession(expired))

		got, err = store.GetSession(live.SessionID)
		assert.NoError(t, err)
		assert.Equal(t, live, got)
		got, err = store.GetSession(expired.SessionID)
		assert.NoError(t, err)
		assert.Nil(t, got)

		advanced := *live
		advanced.Step = "get_parameter_values"
		advanced.ExpiresSec = 1060
		require.NoError(t, store.PutSession(&advanced))
		got, err = store.GetSession(live.SessionID)
		assert.NoError(t, err)
		assert.Equal(t, &advanced, got)

		require.NoError(t, store.DeleteExpiredSessions())
		clock.SetAndFreezeClock(t, time.Unix(2000, 0))
		require.NoError(t, store.DeleteExpiredSessions())
		clock.SetAndFreezeClock(t, time.Unix(1000, 0))
		got, err = store.GetSession(live.SessionID)
		assert.NoError(t, err)
		assert.Nil(t, got)
	})
}
