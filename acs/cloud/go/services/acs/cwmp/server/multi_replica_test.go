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

package server_test

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/simulator"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/tasks"
	"magma/orc8r/cloud/go/sqorc"
)

func TestConcurrentCPEs(t *testing.T) {
	runConcurrentCPEs(t, newHarness(t, defaultConfig(), 3))
}

// NOTE: the Postgres tests need the postgres_test container of the Orc8r test
// environment, or TEST_DATABASE_HOST/TEST_DATABASE_PORT_POSTGRES pointing at
// a Postgres with the magma_test user.
func TestMultiReplica_Postgres_Integration(t *testing.T) {
	runMultiReplica(t, newHarnessOn(t, openPostgres(t, "acs___server"), defaultConfig(), 3))
}

func TestConcurrentCPEs_Postgres_Integration(t *testing.T) {
	runConcurrentCPEs(t, newHarnessOn(t, openPostgres(t, "acs___server_concurrent"), defaultConfig(), 3))
}

// runConcurrentCPEs runs several CPEs at once through three replicas, each
// with its own queued tasks, and checks every task ran once on its device.
func runConcurrentCPEs(t *testing.T, h *harness) {
	const cpes = 6
	sims := make([]*simulator.CPE, cpes)
	for i := range sims {
		id := simID
		id.SerialNumber = fmt.Sprintf("SIM1%03d", i)
		root := datamodel.RootTR181
		if i%2 == 1 {
			root = datamodel.RootTR098
		}
		sims[i] = simulator.New(h.ts.URL+"/acs", root, id, bootstrapUser, bootstrapPass)
	}
	parallel(t, sims, func(c *simulator.CPE) error {
		_, err := c.RunSession(cwmp.EventBootstrap)
		return err
	})

	ids := map[string][]string{}
	for _, c := range sims {
		dev := c.DeviceID.DeviceID()
		ids[dev] = []string{
			h.queueFor(dev, tasks.TypeRefresh, tasks.Args{}, 3),
			h.queueFor(dev, tasks.TypeReboot, tasks.Args{}, 3),
		}
	}
	reqs := sync.Map{}
	parallel(t, sims, func(c *simulator.CPE) error {
		s, err := c.RunSession(cwmp.EventPeriodic)
		reqs.Store(c.DeviceID.DeviceID(), s)
		return err
	})

	for _, c := range sims {
		dev := c.DeviceID.DeviceID()
		creds, err := h.store.GetCredentials(dev)
		require.NoError(t, err)
		assert.NotEmpty(t, creds.ACSPasswordHash, dev)
		for _, id := range ids[dev] {
			task := h.task(id)
			assert.Equal(t, storage.TaskDone, task.Status, dev)
			assert.Equal(t, 1, task.Attempts, dev)
		}
		s, _ := reqs.Load(dev)
		reboots := 0
		for _, m := range s.(*simulator.Session).Requests {
			if r, ok := m.(*cwmp.Reboot); ok {
				reboots++
				assert.Equal(t, ids[dev][1], r.CommandKey)
			}
		}
		assert.Equal(t, 1, reboots, dev)
	}
}

func parallel(t *testing.T, sims []*simulator.CPE, f func(*simulator.CPE) error) {
	wg := sync.WaitGroup{}
	errs := make([]error, len(sims))
	for i, c := range sims {
		wg.Add(1)
		go func(i int, c *simulator.CPE) {
			defer wg.Done()
			errs[i] = f(c)
		}(i, c)
	}
	wg.Wait()
	for i, err := range errs {
		assert.NoError(t, err, sims[i].DeviceID.DeviceID())
	}
}

// openPostgres opens a clean test database and restores SQL_DIALECT after the
// test, so the sqlite tests of the same run keep the no-op locker.
func openPostgres(t *testing.T, name string) *sql.DB {
	t.Setenv(sqorc.SQLDialectEnv, os.Getenv(sqorc.SQLDialectEnv))
	db := sqorc.OpenCleanForTest(t, name, sqorc.PostgresDriver)
	t.Cleanup(func() { db.Close() })
	return db
}
