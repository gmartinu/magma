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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/cwmp/simulator"
	"magma/acs/cloud/go/services/acs/datamodel"
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
	svc := test_init.StartTestService(t, server.Config{
		BootstrapUsername: "bootstrap", BootstrapPassword: "bootstrap-secret", RotateCredentials: true,
	})
	store := svc.Store

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

// End to end through obsidian and the CWMP endpoint: a CPE bootstraps, a
// reboot is queued over REST and runs in the CPE's next session.
func TestACSService_TaskOverCWMP(t *testing.T) {
	svc := test_init.StartTestService(t, server.Config{
		BootstrapUsername: "bootstrap", BootstrapPassword: "bootstrap-secret", RotateCredentials: true,
	})
	pathPrefixesByAddr, err := reverse_proxy.GetEchoServerAddressToPathPrefixes()
	require.NoError(t, err)
	proxy, err := reverse_proxy.NewReverseProxyHandler(nil).AddReverseProxyPaths(echo.New(), pathPrefixesByAddr)
	require.NoError(t, err)
	obsidianSrv := httptest.NewServer(proxy)
	defer obsidianSrv.Close()

	id := cwmp.DeviceIDStruct{Manufacturer: "Global Telecom", OUI: "00A1B2", ProductClass: "Titan4000", SerialNumber: "E2E1"}
	// Claiming devices is not part of the CWMP core yet, so the device is
	// registered to its network before it first connects.
	require.NoError(t, svc.Store.UpsertDevice(&storage.Device{
		DeviceID: id.DeviceID(), NetworkID: "n1", OUI: id.OUI, ProductClass: id.ProductClass, SerialNumber: id.SerialNumber,
	}))
	cpe := simulator.New(svc.CWMPURL+"/", datamodel.RootTR181, id, "bootstrap", "bootstrap-secret")
	s, err := cpe.RunSession(cwmp.EventBootstrap, cwmp.EventBoot)
	require.NoError(t, err)
	require.Len(t, s.Requests, 1, "credential rotation")

	tasksURL := obsidianSrv.URL + "/magma/v1/acs/n1/devices/" + id.DeviceID() + "/tasks"
	resp, err := http.Post(tasksURL, "application/json", strings.NewReader(`{"type":"reboot"}`))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	s, err = cpe.RunSession(cwmp.EventPeriodic)
	require.NoError(t, err)
	require.Len(t, s.Requests, 1)
	assert.IsType(t, &cwmp.Reboot{}, s.Requests[0])

	status, body := get(t, tasksURL)
	assert.Equal(t, http.StatusOK, status)
	var listed []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(body), &listed))
	require.Len(t, listed, 1)
	assert.Equal(t, "reboot", listed[0]["type"])
	assert.Equal(t, "done", listed[0]["status"])
}

func get(t *testing.T, url string) (int, string) {
	resp, err := http.Get(url)
	require.NoError(t, err, fmt.Sprintf("GET %s", url))
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}
