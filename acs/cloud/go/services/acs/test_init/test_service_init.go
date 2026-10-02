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
	"testing"

	"magma/acs/cloud/go/acs"
	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/obsidian/access/tests"
	"magma/orc8r/cloud/go/test_utils"
)

// StartTestService starts the acs service registered with the labels and
// annotations of acs/cloud/configs/service_registry.yml. The caller must
// set the run_echo_server flag for the REST handlers to be served.
func StartTestService(t *testing.T, h *handlers.Handlers) {
	labels := map[string]string{
		orc8r.ObsidianHandlersLabel: "true",
		orc8r.SwaggerSpecLabel:      "true",
	}
	annotations := map[string]string{
		orc8r.ObsidianHandlersPathPrefixesAnnotation: handlers.ManageNetworkPath,
	}
	srv, lis, plis := test_utils.NewTestOrchestratorService(t, acs.ModuleName, acs_service.ServiceName, labels, annotations)
	if srv.EchoServer != nil {
		obsidian.AttachHandlers(srv.EchoServer, h.GetHandlers())
	}
	go srv.RunTest(lis, plis)
	if srv.EchoServer != nil {
		tests.WaitForTestServer(t, srv.EchoServer)
	}
}
