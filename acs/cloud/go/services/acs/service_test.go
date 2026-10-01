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
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/cwmp/simulator"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/test_init"
	"magma/orc8r/cloud/go/clock"
	"magma/orc8r/cloud/go/identity"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/service"
	"magma/orc8r/cloud/go/services/obsidian/access"
	"magma/orc8r/cloud/go/services/obsidian/reverse_proxy"
	lib_protos "magma/orc8r/lib/go/protos"
	"magma/orc8r/lib/go/registry"
	service303 "magma/orc8r/lib/go/service/client"
)

func init() {
	_ = flag.Set(service.RunEchoServerFlag, "true")
}

func TestACSService(t *testing.T) {
	test_init.StartTestService(t, server.Config{
		BootstrapUsername: "bootstrap", BootstrapPassword: "bootstrap-secret", RotateCredentials: true,
	})

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

	// The unclaimed listing is not network scoped: it has its own prefix, so
	// obsidian requires access to every network for it.
	assert.Equal(t, access.SupervisorWildcards(), requestedIdentities(proxy, "/magma/v1/acs/unclaimed"))
	assert.Equal(t, []*lib_protos.Identity{identity.NewNetwork("n1")}, requestedIdentities(proxy, "/magma/v1/acs/n1/devices"))
}

// The MVP flow end to end: a CPE bootstraps and shows up unclaimed, a
// network claims it, a refresh runs, and the device, its raw parameters,
// online state and task history are read back over REST.
func TestACSService_ClaimAndInspect(t *testing.T) {
	clock.SetAndFreezeClock(t, time.Unix(1_000_000, 0))
	defer clock.UnfreezeClock(t)
	svc := test_init.StartTestService(t, server.Config{
		BootstrapUsername: "bootstrap", BootstrapPassword: "bootstrap-secret", RotateCredentials: true,
	})
	api := startObsidian(t) + "/magma/v1/acs"

	id := cwmp.DeviceIDStruct{Manufacturer: "Global Telecom", OUI: "00A1B2", ProductClass: "Titan4000", SerialNumber: "MVP1"}
	dev := id.DeviceID()
	cpe := simulator.New(svc.CWMPURL+"/", datamodel.RootTR181, id, "bootstrap", "bootstrap-secret")
	_, err := cpe.RunSession(cwmp.EventBootstrap, cwmp.EventBoot)
	require.NoError(t, err)

	status, body := get(t, api+"/unclaimed")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[{
		"device_id": "00A1B2-Titan4000-MVP1", "oui": "00A1B2", "product_class": "Titan4000", "serial_number": "MVP1",
		"firmware": "1.0.0-sim", "handler": "generic", "online": true,
		"first_seen": "1970-01-12T13:46:40.000Z", "last_seen": "1970-01-12T13:46:40.000Z"
	}]`, body)
	// Until claimed, nothing but the listing is reachable.
	status, _ = get(t, api+"/n1/devices/"+dev)
	assert.Equal(t, http.StatusNotFound, status)
	status, _ = post(t, api+"/n1/devices/"+dev+"/tasks", `{"type":"refresh"}`)
	assert.Equal(t, http.StatusNotFound, status)

	status, _ = post(t, api+"/n1/devices/"+dev+"/claim", "")
	require.Equal(t, http.StatusOK, status)
	status, _ = post(t, api+"/n2/devices/"+dev+"/claim", "")
	assert.Equal(t, http.StatusConflict, status)
	status, body = get(t, api+"/unclaimed")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, body)

	status, _ = post(t, api+"/n1/devices/"+dev+"/tasks", `{"type":"refresh"}`)
	require.Equal(t, http.StatusCreated, status)
	_, err = cpe.RunSession(cwmp.EventPeriodic)
	require.NoError(t, err)

	status, body = get(t, api+"/n1/devices/"+dev)
	require.Equal(t, http.StatusOK, status)
	detail := struct {
		Device struct {
			Online                 bool  `json:"online"`
			PeriodicInformInterval int64 `json:"periodic_inform_interval"`
		} `json:"device"`
		LastInform struct {
			Events []string `json:"events"`
		} `json:"last_inform"`
		State struct {
			Cellular struct {
				RSRP float64 `json:"rsrp"`
			} `json:"cellular"`
		} `json:"state"`
	}{}
	require.NoError(t, json.Unmarshal([]byte(body), &detail))
	assert.True(t, detail.Device.Online)
	assert.Equal(t, int64(300), detail.Device.PeriodicInformInterval, "read by the refresh")
	assert.Equal(t, []string{cwmp.EventPeriodic}, detail.LastInform.Events)
	assert.Equal(t, -95.0, detail.State.Cellular.RSRP)

	status, body = get(t, api+"/n1/devices/"+dev+"/parameters?prefix=Device.Cellular.Interface.1.RS")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[
		{"name": "Device.Cellular.Interface.1.RSRP", "value": "-95", "updated": "1970-01-12T13:46:40.000Z"},
		{"name": "Device.Cellular.Interface.1.RSRQ", "value": "-11", "updated": "1970-01-12T13:46:40.000Z"}
	]`, body)
	status, body = get(t, api+"/n1/devices/"+dev+"/parameters?prefix=Device.DeviceInfo.SerialNumber")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, `"value":"MVP1"`, "Inform parameters are kept too")

	status, body = get(t, api+"/n1/devices/"+dev+"/tasks")
	require.Equal(t, http.StatusOK, status)
	var history []map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(body), &history))
	require.Len(t, history, 1)
	assert.Equal(t, "refresh", history[0]["type"])
	assert.Equal(t, "done", history[0]["status"])

	// Two periodic intervals (2 x 300s) without an Inform take it offline.
	// The Inform carries no model name; the refresh read it.
	status, body = get(t, api+"/n1/devices?online=true&model=Titan4000")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, dev)
	clock.SetAndFreezeClock(t, time.Unix(1_000_000+601, 0))
	status, body = get(t, api+"/n1/devices?online=true")
	require.Equal(t, http.StatusOK, status)
	assert.JSONEq(t, `[]`, body)
	status, body = get(t, api+"/n1/devices?online=false")
	require.Equal(t, http.StatusOK, status)
	assert.Contains(t, body, dev)
}

func startObsidian(t *testing.T) string {
	pathPrefixesByAddr, err := reverse_proxy.GetEchoServerAddressToPathPrefixes()
	require.NoError(t, err)
	proxy, err := reverse_proxy.NewReverseProxyHandler(nil).AddReverseProxyPaths(echo.New(), pathPrefixesByAddr)
	require.NoError(t, err)
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	return srv.URL
}

// requestedIdentities are the identities obsidian's access middleware
// requires for a GET of path, from the route the proxy matches.
func requestedIdentities(e *echo.Echo, path string) []*lib_protos.Identity {
	c := e.NewContext(httptest.NewRequest(http.MethodGet, path, nil), httptest.NewRecorder())
	e.Router().Find(http.MethodGet, path, c)
	return access.FindRequestedIdentities(c)
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
	cpe := simulator.New(svc.CWMPURL+"/", datamodel.RootTR181, id, "bootstrap", "bootstrap-secret")
	s, err := cpe.RunSession(cwmp.EventBootstrap, cwmp.EventBoot)
	require.NoError(t, err)
	require.Len(t, s.Requests, 1, "credential rotation")
	status, _ := post(t, obsidianSrv.URL+"/magma/v1/acs/n1/devices/"+id.DeviceID()+"/claim", "")
	require.Equal(t, http.StatusOK, status)

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

func post(t *testing.T, url, body string) (int, string) {
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	require.NoError(t, err, fmt.Sprintf("POST %s", url))
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(b)
}

func get(t *testing.T, url string) (int, string) {
	resp, err := http.Get(url)
	require.NoError(t, err, fmt.Sprintf("GET %s", url))
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}
