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

// NOTE: this test needs the postgres_test container of the Orc8r test
// environment. To run it elsewhere, point it at a Postgres with the
// magma_test user, e.g.
//	- TEST_DATABASE_HOST=localhost
//	- TEST_DATABASE_PORT_POSTGRES=5433

package storage_test

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/sqorc"
)

func TestSQLACSStorage_Postgres_Integration(t *testing.T) {
	db := openPostgres(t, "acs___storage")
	runStorageTests(t, storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder()))
}

// TestClaimNextTask_Postgres_Concurrent claims from several connections at
// once, as replicas would, and checks that no task is handed out twice.
func TestClaimNextTask_Postgres_Concurrent(t *testing.T) {
	db := openPostgres(t, "acs___claim")
	store := storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder())
	require.NoError(t, store.Init())
	const dev, tasks, workers = "dev1", 20, 8
	require.NoError(t, store.UpsertDevice(&storage.Device{DeviceID: dev, OUI: "O", SerialNumber: "S"}))
	for i := 0; i < tasks; i++ {
		require.NoError(t, store.CreateTask(&storage.Task{
			TaskID: fmt.Sprintf("t%02d", i), DeviceID: dev, Type: "reboot", Args: "{}", Status: storage.TaskPending, MaxAttempts: 1,
		}))
	}

	var mu sync.Mutex
	claimed := map[string]int{}
	wg := sync.WaitGroup{}
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for misses, errs := 0, 0; misses < 3 && errs < 50; {
				task, err := store.ClaimNextTask(dev, fmt.Sprintf("s%d", w))
				if err != nil {
					errs++
					continue
				}
				if task == nil {
					misses++
					continue
				}
				mu.Lock()
				claimed[task.TaskID]++
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
	assert.Len(t, claimed, tasks)
	for id, n := range claimed {
		assert.Equal(t, 1, n, id)
	}
}

// openPostgres opens a clean test database. SQL_DIALECT is restored after the
// test so that the sqlite tests of the same run do not get Postgres locking.
func openPostgres(t *testing.T, name string) *sql.DB {
	t.Setenv(sqorc.SQLDialectEnv, os.Getenv(sqorc.SQLDialectEnv))
	db := sqorc.OpenCleanForTest(t, name, sqorc.PostgresDriver)
	t.Cleanup(func() { db.Close() })
	return db
}
