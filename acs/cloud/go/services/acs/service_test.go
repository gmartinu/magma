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

package acs_test

import (
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/test_init"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/service"
	configurator_test_init "magma/orc8r/cloud/go/services/configurator/test_init"
	entitlements_servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	entitlements_test_init "magma/orc8r/cloud/go/services/entitlements/test_init"
	"magma/orc8r/cloud/go/services/obsidian/reverse_proxy"
	state_test_init "magma/orc8r/cloud/go/services/state/test_init"
	lib_protos "magma/orc8r/lib/go/protos"
	"magma/orc8r/lib/go/registry"
	service303 "magma/orc8r/lib/go/service/client"
)

func init() {
	_ = flag.Set(service.RunEchoServerFlag, "true")
}

func TestACSServiceThroughObsidian(t *testing.T) {
	configurator_test_init.StartTestService(t)
	// The acs routes check the entitlement; not enforced, they all pass.
	entitlements_test_init.StartTestService(t, entitlements_servicers.Config{}, nil)
	state_test_init.StartTestService(t)
	test_init.StartTestService(t, handlers.NewHandlers(nil))

	info, err := service303.Service303GetServiceInfo(acs_service.ServiceName)
	require.NoError(t, err)
	assert.Equal(t, lib_protos.ServiceInfo_ALIVE, info.State)
	for _, label := range []string{orc8r.ObsidianHandlersLabel, orc8r.SwaggerSpecLabel} {
		services, err := registry.FindServices(label)
		require.NoError(t, err)
		assert.Contains(t, services, acs_service.ServiceName)
	}

	// Route the way obsidian does in a deployment: through its reverse proxy,
	// built from the registry's path prefix annotations.
	pathPrefixesByAddr, err := reverse_proxy.GetEchoServerAddressToPathPrefixes()
	require.NoError(t, err)
	proxy, err := reverse_proxy.NewReverseProxyHandler(nil).AddReverseProxyPaths(echo.New(), pathPrefixesByAddr)
	require.NoError(t, err)
	obsidianSrv := httptest.NewServer(proxy)
	defer obsidianSrv.Close()

	resp, err := http.Get(obsidianSrv.URL + "/magma/v1/acs/n1/cpes")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	assert.JSONEq(t, `[]`, string(body))
}
