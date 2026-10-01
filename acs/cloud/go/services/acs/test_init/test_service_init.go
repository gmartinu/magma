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

package test_init

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/acs"
	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/obsidian/access/tests"
	"magma/orc8r/cloud/go/sqorc"
	"magma/orc8r/cloud/go/test_utils"
)

// TestService is a running acs service.
type TestService struct {
	Store storage.ACSStorage
	// CWMPURL is the CWMP endpoint CPEs connect to.
	CWMPURL string
}

// StartTestService starts the acs service on sqlite, registered with the same
// labels and annotations as in acs/cloud/configs/service_registry.yml, with
// its CWMP endpoint accepting cwmpConfig's bootstrap credentials. The caller
// must set the run_echo_server flag for the REST handlers to be served.
func StartTestService(t *testing.T, cwmpConfig server.Config) *TestService {
	labels := map[string]string{
		orc8r.ObsidianHandlersLabel: "true",
		orc8r.SwaggerSpecLabel:      "true",
	}
	annotations := map[string]string{
		orc8r.ObsidianHandlersPathPrefixesAnnotation: handlers.ManageNetworkPath,
	}
	srv, lis, plis := test_utils.NewTestOrchestratorService(t, acs.ModuleName, acs_service.ServiceName, labels, annotations)

	db, err := sqorc.Open("sqlite3", ":memory:?_foreign_keys=1")
	require.NoError(t, err)
	// Every connection to :memory: is its own database.
	db.SetMaxOpenConns(1)
	sealer, err := storage.NewSealer(make([]byte, storage.EncryptionKeySize))
	require.NoError(t, err)
	store := storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder(), storage.WithSealer(sealer))
	require.NoError(t, store.Init())

	if srv.EchoServer != nil {
		obsidian.AttachHandlers(srv.EchoServer, handlers.NewHandlers(store).GetHandlers())
	}

	cwmpServer, err := server.New(cwmpConfig, store, datamodel.NewRegistry())
	require.NoError(t, err)
	cwmp := httptest.NewServer(cwmpServer)
	t.Cleanup(cwmp.Close)

	go srv.RunTest(lis, plis)
	if srv.EchoServer != nil {
		tests.WaitForTestServer(t, srv.EchoServer)
	}
	return &TestService{Store: store, CWMPURL: cwmp.URL}
}
