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
	"strconv"
	"testing"

	"magma/acs/cloud/go/acs"
	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/reports"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/obsidian/access/tests"
	state_protos "magma/orc8r/cloud/go/services/state/protos"
	"magma/orc8r/cloud/go/test_utils"
)

// StartTestService starts the acs service registered with the labels and
// annotations of acs/cloud/configs/service_registry.yml. The caller must
// set the run_echo_server flag for the REST handlers to be served. The
// service indexes cpe_acs into store.
func StartTestService(t *testing.T, h *handlers.Handlers, store *reports.Store) {
	labels := map[string]string{
		orc8r.ObsidianHandlersLabel: "true",
		orc8r.StateIndexerLabel:     "true",
		orc8r.SwaggerSpecLabel:      "true",
	}
	annotations := map[string]string{
		orc8r.ObsidianHandlersPathPrefixesAnnotation: handlers.ManageNetworkPath,
		orc8r.StateIndexerTypesAnnotation:            reports.IndexerTypes[0],
		orc8r.StateIndexerVersionAnnotation:          strconv.Itoa(int(reports.IndexerVersion)),
	}
	srv, lis, plis := test_utils.NewTestOrchestratorService(t, acs.ModuleName, acs_service.ServiceName, labels, annotations)
	state_protos.RegisterIndexerServer(srv.ProtectedGrpcServer, reports.NewIndexerServicer(store))
	if srv.EchoServer != nil {
		obsidian.AttachHandlers(srv.EchoServer, h.GetHandlers())
	}
	go srv.RunTest(lis, plis)
	if srv.EchoServer != nil {
		tests.WaitForTestServer(t, srv.EchoServer)
	}
}
