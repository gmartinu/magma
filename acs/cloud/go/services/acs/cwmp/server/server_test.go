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
	"magma/acs/cloud/go/services/acs/cwmp/simulator"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/tasks"
	"magma/orc8r/cloud/go/clock"
)

func TestBootstrapRotationAndPeriodic(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)

	s := runSession(t, cpe, cwmp.EventBootstrap, cwmp.EventBoot)
	require.Equal(t, []string{"SetParameterValues"}, methods(s))
	spv := s.Requests[0].(*cwmp.SetParameterValues)
	values := spv.ParameterList.Map()
	assert.Equal(t, h.deviceID(), values["Device.ManagementServer.Username"])
	assert.Len(t, values["Device.ManagementServer.Password"], datamodel.DefaultPasswordLength)
	assert.Equal(t, h.deviceID(), values["Device.ManagementServer.ConnectionRequestUsername"])
	assert.Equal(t, h.deviceID(), cpe.Username)
	assert.NotEqual(t, bootstrapPass, cpe.Password)

	creds, err := h.store.GetCredentials(h.deviceID())
	require.NoError(t, err)
	assert.Equal(t, auth.HashPassword(h.deviceID(), "magma-acs", cpe.Password), creds.ACSPasswordHash)
	assert.Empty(t, creds.PendingPasswordHash)
	assert.NotContains(t, creds.ACSPasswordHash, cpe.Password)

	device, err := h.store.GetDevice(h.deviceID())
	require.NoError(t, err)
	assert.Equal(t, "1.0.0-sim", device.Firmware)
	assert.Equal(t, datamodel.GenericHandlerName, device.Handler)
	assert.Equal(t, "http://192.0.2.10:7548/cr", device.ConnectionRequestURL)
	assert.Equal(t, datamodel.RootTR181, h.model().Root)

	// With its own credentials the CPE has nothing more to do.
	s = runSession(t, cpe, cwmp.EventPeriodic)
	assert.Empty(t, s.Requests)

	// The bootstrap credentials no longer work for this device.
	impostor := h.cpe(datamodel.RootTR181)
	s, err = impostor.RunSession(cwmp.EventPeriodic)
	assert.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, s.Status)

	// A wrong bootstrap password is refused for an unknown device too.
	other := h.cpe(datamodel.RootTR181)
	other.DeviceID.SerialNumber = "SIM0002"
	other.Password = "wrong"
	s, err = other.RunSession(cwmp.EventBootstrap)
	assert.Error(t, err)
	assert.Equal(t, http.StatusUnauthorized, s.Status)

	sess, err := h.store.GetDeviceSession(h.deviceID())
	require.NoError(t, err)
	assert.Nil(t, sess, "sessions end with the 204")
}

func TestRotationAnswerLost(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)
	cpe.ApplyAndDrop = "SetParameterValues"
	_, err := cpe.RunSession(cwmp.EventBootstrap)
	require.NoError(t, err)
	creds, _ := h.store.GetCredentials(h.deviceID())
	assert.Empty(t, creds.ACSPasswordHash)
	assert.NotEmpty(t, creds.PendingPasswordHash)

	// The CPE applied the new password: the ACS accepts it and promotes it.
	cpe.ApplyAndDrop = ""
	s := runSession(t, cpe, cwmp.EventPeriodic)
	assert.Empty(t, s.Requests)
	creds, _ = h.store.GetCredentials(h.deviceID())
	assert.Equal(t, auth.HashPassword(h.deviceID(), "magma-acs", cpe.Password), creds.ACSPasswordHash)
	assert.Empty(t, creds.PendingPasswordHash)
}

func TestRotationRefused(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR098)
	cpe.Faults["SetParameterValues"] = cwmp.NewFault(cwmp.FaultCPENonWritableParam, "read only")
	s := runSession(t, cpe, cwmp.EventBootstrap)
	require.Equal(t, []string{"SetParameterValues"}, methods(s))
	assert.Equal(t, "InternetGatewayDevice.ManagementServer.Password", s.Requests[0].(*cwmp.SetParameterValues).ParameterList[1].Name)
	creds, _ := h.store.GetCredentials(h.deviceID())
	assert.Empty(t, creds.PendingPasswordHash)
	// Still on bootstrap credentials, so the ACS tries again next session.
	s = runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, []string{"SetParameterValues"}, methods(s))
}

func TestTasksDrainedOnInform(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)
	runSession(t, cpe, cwmp.EventBootstrap)

	reboot := h.queue(tasks.TypeReboot, tasks.Args{}, 3)
	refresh := h.queue(tasks.TypeRefresh, tasks.Args{}, 3)
	gpv := h.queue(tasks.TypeGetParameterValues, tasks.Args{ParameterNames: []string{"Device.DeviceInfo.UpTime", "Device.Cellular.Interface.1.RSRP"}}, 3)
	spv := h.queue(tasks.TypeSetParameterValues, tasks.Args{ParameterValues: []tasks.ParameterValue{
		{Name: "Device.ManagementServer.PeriodicInformInterval", Value: "600", Type: "xsd:unsignedInt"},
	}}, 3)
	gpn := h.queue(tasks.TypeGetParameterNames, tasks.Args{ParameterPath: "Device.Cellular.", NextLevel: true}, 3)

	s := runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, []string{
		"Reboot",
		"GetParameterValues", "GetParameterValues", "GetParameterValues", "GetParameterValues",
		"GetParameterValues", "SetParameterValues", "GetParameterNames",
	}, methods(s))
	assert.Equal(t, &cwmp.Reboot{CommandKey: reboot}, s.Requests[0])

	for _, id := range []string{reboot, refresh, gpv, spv, gpn} {
		task := h.task(id)
		assert.Equal(t, storage.TaskDone, task.Status, id)
		assert.Equal(t, 1, task.Attempts)
	}
	res, err := tasks.ParseResult(h.task(gpv).Result)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"Device.DeviceInfo.UpTime": "100", "Device.Cellular.Interface.1.RSRP": "-95"}, res.Values)
	res, _ = tasks.ParseResult(h.task(spv).Result)
	assert.Equal(t, 0, *res.Status)
	assert.Equal(t, "600", cpe.Params["Device.ManagementServer.PeriodicInformInterval"].Value)
	res, _ = tasks.ParseResult(h.task(gpn).Result)
	assert.Contains(t, res.Names, tasks.ParameterInfo{Name: "Device.Cellular.AccessPoint."})

	m := h.model()
	assert.Equal(t, -95.0, *m.Cellular.RSRP)
	assert.Equal(t, 14.0, *m.Cellular.SINR)
	assert.Equal(t, "n71", m.Cellular.Band)
	assert.Equal(t, "100.64.0.9", m.WAN.IPv4Address)
	assert.Equal(t, "SIM4000", m.Identity.ModelName)

	// The reboot shows up as events of the next session.
	cpe.Params["Device.DeviceInfo.SoftwareVersion"] = simulator.Param{Value: "2.0.0", Type: "xsd:string"}
	s = runSession(t, cpe)
	assert.Empty(t, s.Requests)
	device, _ := h.store.GetDevice(h.deviceID())
	assert.Equal(t, "2.0.0", device.Firmware)
}

func TestRefreshToleratesMissingObjects(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR098)
	runSession(t, cpe, cwmp.EventBootstrap)
	delete(cpe.Params, "InternetGatewayDevice.WANDevice.1.WANConnectionDevice.1.WANIPConnection.1.ExternalIPAddress")
	id := h.queue(tasks.TypeRefresh, tasks.Args{}, 1)
	s := runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, []string{"GetParameterValues", "GetParameterValues", "GetParameterValues"}, methods(s))
	task := h.task(id)
	assert.Equal(t, storage.TaskDone, task.Status)
	res, _ := tasks.ParseResult(task.Result)
	require.Len(t, res.Faults, 1)
	assert.Equal(t, cwmp.FaultCPEInvalidParamName, res.Faults[0].FaultCode)
	assert.Equal(t, "GetParameterValues InternetGatewayDevice.WANDevice.", res.Faults[0].Request)
	assert.Equal(t, datamodel.RootTR098, h.model().Root)
	assert.Equal(t, "1.0.0-sim", h.model().Firmware.SoftwareVersion)
}

func TestTaskFaults(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)
	runSession(t, cpe, cwmp.EventBootstrap)

	readOnly := h.queue(tasks.TypeSetParameterValues, tasks.Args{ParameterValues: []tasks.ParameterValue{{Name: "Device.DeviceInfo.SerialNumber", Value: "x"}}}, 3)
	missing := h.queue(tasks.TypeGetParameterValues, tasks.Args{ParameterNames: []string{"Device.Nope"}}, 3)
	flaky := h.queue(tasks.TypeReboot, tasks.Args{}, 2)
	after := h.queue(tasks.TypeGetParameterValues, tasks.Args{ParameterNames: []string{"Device.DeviceInfo.UpTime"}}, 3)
	cpe.Faults["Reboot"] = cwmp.NewFault(cwmp.FaultCPEInternalError, "busy")

	s := runSession(t, cpe, cwmp.EventPeriodic)
	// A fault ends only its task; the session goes on with the next one.
	assert.Equal(t, []string{"SetParameterValues", "GetParameterValues", "Reboot", "GetParameterValues"}, methods(s))

	task := h.task(readOnly)
	assert.Equal(t, storage.TaskFailed, task.Status)
	assert.Equal(t, cwmp.FaultCPEInvalidArguments, task.FaultCode)
	assert.Equal(t, "Invalid arguments", task.FaultString)
	task = h.task(missing)
	assert.Equal(t, storage.TaskFailed, task.Status)
	assert.Equal(t, cwmp.FaultCPEInvalidParamName, task.FaultCode)
	// 9002 is transient: the task waits for the next session.
	task = h.task(flaky)
	assert.Equal(t, storage.TaskPending, task.Status)
	assert.Equal(t, 1, task.Attempts)
	assert.Equal(t, "busy", task.FaultString)
	assert.Equal(t, storage.TaskDone, h.task(after).Status)

	s = runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, []string{"Reboot"}, methods(s))
	task = h.task(flaky)
	assert.Equal(t, storage.TaskFailed, task.Status)
	assert.Equal(t, 2, task.Attempts)

	// A malformed stored task fails without reaching the CPE.
	require.NoError(t, h.store.CreateTask(&storage.Task{TaskID: "bad", DeviceID: h.deviceID(), Type: "upgrade", Args: "{}", Status: storage.TaskPending, MaxAttempts: 1}))
	s = runSession(t, cpe, cwmp.EventPeriodic)
	assert.Empty(t, s.Requests)
	assert.Equal(t, storage.TaskFailed, h.task("bad").Status)
}

func TestSessionTimeoutRequeuesTask(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)
	runSession(t, cpe, cwmp.EventBootstrap)

	first := h.queue(tasks.TypeGetParameterValues, tasks.Args{ParameterNames: []string{"Device.DeviceInfo.UpTime"}}, 3)
	reboot := h.queue(tasks.TypeReboot, tasks.Args{}, 3)
	cpe.StopAfter = 1
	s, err := cpe.RunSession(cwmp.EventPeriodic)
	require.NoError(t, err)
	assert.Equal(t, []string{"GetParameterValues", "Reboot"}, methods(s))
	assert.Equal(t, storage.TaskDone, h.task(first).Status)
	assert.Equal(t, storage.TaskInProgress, h.task(reboot).Status)

	clock.SetAndFreezeClock(t, time.Now().Add(2*time.Minute))
	defer clock.UnfreezeClock(t)
	res, err := h.store.ReapExpired()
	require.NoError(t, err)
	assert.Equal(t, storage.ReapResult{Sessions: 1, RequeuedTasks: 1}, res)
	assert.Equal(t, storage.TaskPending, h.task(reboot).Status)

	cpe.StopAfter = 0
	s = runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, []string{"Reboot"}, methods(s))
	task := h.task(reboot)
	assert.Equal(t, storage.TaskDone, task.Status)
	assert.Equal(t, 2, task.Attempts)
}

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

func TestFactoryResetReturnsToBootstrap(t *testing.T) {
	h := newHarness(t, defaultConfig(), 1)
	cpe := h.cpe(datamodel.RootTR181)
	runSession(t, cpe, cwmp.EventBootstrap)
	id := h.queue(tasks.TypeFactoryReset, tasks.Args{}, 1)
	s := runSession(t, cpe, cwmp.EventPeriodic)
	assert.Equal(t, []string{"FactoryReset"}, methods(s))
	assert.Equal(t, storage.TaskDone, h.task(id).Status)
	assert.Equal(t, bootstrapUser, cpe.Username)

	s = runSession(t, cpe)
	assert.Equal(t, []string{"SetParameterValues"}, methods(s), "rotated again after the reset")
	assert.Equal(t, h.deviceID(), cpe.Username)
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
