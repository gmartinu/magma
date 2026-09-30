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
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/auth"
	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/tasks"
)

func TestNewInformEndsOpenSession(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)
	runSession(t, cpe, cwmp.EventBootstrap)
	reboot := h.queue(tasks.TypeReboot, tasks.Args{}, 3)
	cpe.ApplyAndDrop = "Reboot"
	_, err := cpe.RunSession(cwmp.EventPeriodic)
	require.NoError(t, err)
	assert.Equal(t, storage.TaskInProgress, h.task(reboot).Status)

	// The CPE rebooted without answering: its BOOT Inform requeues the task,
	// which then runs again in the new session.
	cpe.ApplyAndDrop = ""
	s := runSession(t, cpe)
	assert.Equal(t, []string{"Reboot"}, methods(s))
	assert.Equal(t, storage.TaskDone, h.task(reboot).Status)
}

func TestCookielessCPE(t *testing.T) {
	h := newHarness(t, defaultConfig(), 2)
	cpe := h.cpe(datamodel.RootTR181)
	runSession(t, cpe, cwmp.EventBootstrap)

	// Without the cookie the session is found by the device the credentials
	// belong to, never by the source address.
	cpe.DisableCookies()
	id := h.queue(tasks.TypeReboot, tasks.Args{}, 1)
	s := runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, []string{"Reboot"}, methods(s))
	assert.Equal(t, storage.TaskDone, h.task(id).Status)

	// Bootstrap credentials do not name a device, so a cookie-less CPE on
	// them gets its Inform answered and the session ends there, without the
	// rotation.
	fresh := h.cpe(datamodel.RootTR181)
	fresh.DeviceID.SerialNumber = "SIM0003"
	fresh.DisableCookies()
	s = runSession(t, fresh, cwmp.EventBootstrap)
	assert.Empty(t, s.Requests)
	creds, err := h.store.GetCredentials(fresh.DeviceID.DeviceID())
	require.NoError(t, err)
	assert.Nil(t, creds)
}

func TestNamespaceEcho(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)
	cpe.Namespace = cwmp.NSCwmp12
	s := runSession(t, cpe, cwmp.EventBootstrap)
	assert.Equal(t, cwmp.NSCwmp12, s.Namespace)
}

func TestBasicAuthPolicy(t *testing.T) {
	// Behind the ingress proxy with TLS: Basic is offered and accepted.
	cfg := defaultConfig()
	cfg.TrustProxyHeaders = true
	h := newHarness(t, cfg, 1)
	cpe := h.cpe(datamodel.RootTR181)
	cpe.PreferBasic = true
	cpe.Headers["X-Forwarded-Proto"] = "https"
	runSession(t, cpe, cwmp.EventBootstrap)
	runSession(t, cpe, cwmp.EventPeriodic)

	// Same header without the trust flag: plain HTTP, Digest only.
	h = newHarness(t, defaultConfig(), 1)
	resp := rawPost(t, h, informBody(t), map[string]string{
		"X-Forwarded-Proto": "https",
		"Authorization":     auth.BasicAuthorization(bootstrapUser, bootstrapPass),
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.Len(t, resp.Header.Values("WWW-Authenticate"), 1)

	// The plain listener of the proxy says http: Digest only as well.
	h = newHarness(t, cfg, 1)
	resp = rawPost(t, h, informBody(t), map[string]string{
		"X-Forwarded-Proto": "http",
		"Authorization":     auth.BasicAuthorization(bootstrapUser, bootstrapPass),
	})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// A Digest CPE works with the default policy.
	cpe = h.cpe(datamodel.RootTR181)
	cpe.PreferBasic = true
	runSession(t, cpe, cwmp.EventBootstrap)
}

func TestInformRateLimit(t *testing.T) {
	cfg := defaultConfig()
	cfg.InformRateLimit = 2
	cfg.InformRateWindow = time.Hour
	h := newHarness(t, cfg, 1)
	cpe := h.cpe(datamodel.RootTR181)
	runSession(t, cpe, cwmp.EventBootstrap)
	runSession(t, cpe, cwmp.EventPeriodic)
	s, err := cpe.RunSession(cwmp.EventPeriodic)
	assert.Error(t, err)
	assert.Equal(t, http.StatusServiceUnavailable, s.Status)
}

func TestProtocolEdges(t *testing.T) {
	cfg := defaultConfig()
	cfg.BasicPolicy = auth.BasicAlways
	h := newHarness(t, cfg, 1)
	basic := map[string]string{"Authorization": auth.BasicAuthorization(bootstrapUser, bootstrapPass)}

	resp, err := http.Get(h.ts.URL)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusMethodNotAllowed, resp.StatusCode)

	assert.Equal(t, http.StatusBadRequest, rawPost(t, h, []byte("<not-soap"), nil).StatusCode)
	assert.Equal(t, http.StatusNoContent, rawPost(t, h, nil, basic).StatusCode, "empty POST without session")
	rpc, _ := cwmp.Encode(&cwmp.Envelope{ID: "1", Body: &cwmp.GetRPCMethods{}})
	assert.Equal(t, http.StatusUnauthorized, rawPost(t, h, rpc, nil).StatusCode)

	noSerial, _ := cwmp.Encode(&cwmp.Envelope{ID: "1", Body: &cwmp.Inform{DeviceId: cwmp.DeviceIDStruct{OUI: "X"}}})
	assert.Equal(t, http.StatusBadRequest, rawPost(t, h, noSerial, basic).StatusCode)

	// Inform, then CPE requests inside the session.
	resp = rawPost(t, h, informBody(t), basic)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	cookies := resp.Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, server.CookieName, cookies[0].Name)
	withCookie := map[string]string{"Cookie": cookies[0].Name + "=" + cookies[0].Value}

	env := rawCWMP(t, h, &cwmp.GetRPCMethods{}, withCookie)
	assert.Equal(t, &cwmp.GetRPCMethodsResponse{MethodList: cwmp.StringList{"Inform", "GetRPCMethods", "TransferComplete"}}, env.Body)
	env = rawCWMP(t, h, &cwmp.TransferComplete{CommandKey: "dl"}, withCookie)
	assert.Equal(t, &cwmp.TransferCompleteResponse{}, env.Body)
	kicked := []byte(`<e:Envelope xmlns:e="http://schemas.xmlsoap.org/soap/envelope/" xmlns:c="urn:dslforum-org:cwmp-1-0"><e:Header><c:ID>k</c:ID></e:Header><e:Body><c:Kicked/></e:Body></e:Envelope>`)
	resp = rawPost(t, h, kicked, withCookie)
	env = decodeResp(t, resp)
	assert.Equal(t, uint32(cwmp.FaultMethodNotSupported), env.Body.(*cwmp.Fault).Code())
	assert.Equal(t, "k", env.ID)

	// The rotation request; answering it with something else is a protocol
	// slip, after which the session ends.
	resp = rawPost(t, h, nil, withCookie)
	env = decodeResp(t, resp)
	require.IsType(t, &cwmp.SetParameterValues{}, env.Body)
	env = rawCWMP(t, h, &cwmp.RebootResponse{}, withCookie)
	assert.Nil(t, env)

	// Credentials of one device used by another.
	creds, _ := h.store.GetCredentials(h.deviceID())
	creds.ACSPasswordHash = auth.HashPassword(creds.ACSUsername, "magma-acs", "pw")
	require.NoError(t, h.store.PutCredentials(creds))
	other := &cwmp.Inform{DeviceId: cwmp.DeviceIDStruct{OUI: "00A1B2", SerialNumber: "OTHER"}}
	body, _ := cwmp.Encode(&cwmp.Envelope{ID: "1", Body: other})
	resp = rawPost(t, h, body, map[string]string{"Authorization": auth.BasicAuthorization(creds.ACSUsername, "pw")})
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
