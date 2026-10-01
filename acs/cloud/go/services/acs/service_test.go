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
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/test_init"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/service"
	"magma/orc8r/cloud/go/services/obsidian/reverse_proxy"
	lib_protos "magma/orc8r/lib/go/protos"
	"magma/orc8r/lib/go/registry"
	service303 "magma/orc8r/lib/go/service/client"
)

func init() {
	_ = flag.Set(service.RunEchoServerFlag, "true")
}

func TestACSService(t *testing.T) {
	store := test_init.StartTestService(t)

	info, err := service303.Service303GetServiceInfo(acs_service.ServiceName)
	require.NoError(t, err)
	assert.Equal(t, acs_service.ServiceName, info.Name)
	assert.Equal(t, lib_protos.ServiceInfo_ALIVE, info.State)

	obsidianServices, err := registry.FindServices(orc8r.ObsidianHandlersLabel)
	require.NoError(t, err)
	assert.Contains(t, obsidianServices, acs_service.ServiceName)
	specServices, err := registry.FindServices(orc8r.SwaggerSpecLabel)
	require.NoError(t, err)
	assert.Contains(t, specServices, acs_service.ServiceName)

	// Route the request the way obsidian does in a deployment: through its
	// reverse proxy, built from the registry's path prefix annotations.
	pathPrefixesByAddr, err := reverse_proxy.GetEchoServerAddressToPathPrefixes()
	require.NoError(t, err)
	proxy, err := reverse_proxy.NewReverseProxyHandler(nil).AddReverseProxyPaths(echo.New(), pathPrefixesByAddr)
	require.NoError(t, err)
	obsidianSrv := httptest.NewServer(proxy)
	defer obsidianSrv.Close()

	status, body := get(t, obsidianSrv.URL+"/magma/v1/acs/n1/devices")
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, body)

	require.NoError(t, store.UpsertDevice(&storage.Device{
		DeviceID:     "00259E-Titan4000-SN1",
		NetworkID:    "n1",
		OUI:          "00259E",
		ProductClass: "Titan4000",
		SerialNumber: "SN1",
		FirstSeenSec: 0,
		LastSeenSec:  0,
	}))
	status, body = get(t, obsidianSrv.URL+"/magma/v1/acs/n1/devices")
	assert.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[{
		"device_id": "00259E-Titan4000-SN1",
		"oui": "00259E",
		"product_class": "Titan4000",
		"serial_number": "SN1",
		"first_seen": "1970-01-01T00:00:00.000Z",
		"last_seen": "1970-01-01T00:00:00.000Z"
	}]`, body)
}

func get(t *testing.T, url string) (int, string) {
	resp, err := http.Get(url)
	require.NoError(t, err, fmt.Sprintf("GET %s", url))
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}
