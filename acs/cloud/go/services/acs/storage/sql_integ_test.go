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
	"testing"

	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/sqorc"
)

func TestSQLACSStorage_Postgres_Integration(t *testing.T) {
	db := sqorc.OpenCleanForTest(t, "acs___storage", sqorc.PostgresDriver)
	defer db.Close()
	runStorageTests(t, storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder()))
}
