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
	"context"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/reports"
	"magma/acs/cloud/go/services/acs/test_init"
	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/service"
	"magma/orc8r/cloud/go/services/configurator"
	configurator_test_init "magma/orc8r/cloud/go/services/configurator/test_init"
	device_test_init "magma/orc8r/cloud/go/services/device/test_init"
	entitlements_servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	entitlements_test_init "magma/orc8r/cloud/go/services/entitlements/test_init"
	"magma/orc8r/cloud/go/services/obsidian/reverse_proxy"
	state_test_init "magma/orc8r/cloud/go/services/state/test_init"
	state_test_utils "magma/orc8r/cloud/go/services/state/test_utils"
	"magma/orc8r/cloud/go/test_utils"
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
	factory := test_utils.NewSQLBlobstore(t, "acs_service_test_reports")
	require.NoError(t, factory.InitializeFactory())
	store := reports.NewStore(factory)
	test_init.StartTestService(t, handlers.NewHandlers(nil, store), store)

	info, err := service303.Service303GetServiceInfo(acs_service.ServiceName)
	require.NoError(t, err)
	assert.Equal(t, lib_protos.ServiceInfo_ALIVE, info.State)
	for _, label := range []string{orc8r.ObsidianHandlersLabel, orc8r.StateIndexerLabel, orc8r.SwaggerSpecLabel} {
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

// The state service hands the cpe_acs state a gateway reports to the acs
// service, as registered by its indexer labels.
func TestACSServiceIndexesReportedCpeState(t *testing.T) {
	configurator_test_init.StartTestService(t)
	device_test_init.StartTestService(t)
	state_test_init.StartTestService(t)
	factory := test_utils.NewSQLBlobstore(t, "acs_service_test_index")
	require.NoError(t, factory.InitializeFactory())
	store := reports.NewStore(factory)
	test_init.StartTestService(t, handlers.NewHandlers(nil, store), store)

	ctx := context.Background()
	require.NoError(t, configurator.CreateNetwork(ctx, configurator.Network{ID: "n1", Type: lte.NetworkType}, nil))
	_, err := configurator.CreateEntity(ctx, "n1", configurator.NetworkEntity{Type: orc8r.MagmadGatewayType, Key: "g1", PhysicalID: "hw1"}, nil)
	require.NoError(t, err)

	gwCtx := state_test_utils.GetContextWithCertificate(t, "hw1")
	view := &cpestate.CpeView{CpeKey: "IMSI001010000000001", Mode: cpestate.ModeCore, LastInform: 1700000000}
	state_test_utils.ReportState(t, gwCtx, lte.CPEAcsStateType, view.CpeKey, view, cpestate.Serdes)

	require.Eventually(t, func() bool {
		rs, err := store.Get("n1", view.CpeKey)
		return err == nil && len(rs) == 1 && rs[0].HardwareID == "hw1"
	}, 10*time.Second, 50*time.Millisecond)
}
