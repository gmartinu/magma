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

package datamodel_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/capture"
	"magma/acs/cloud/go/services/acs/datamodel"
)

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }
func b(v bool) *bool       { return &v }

func TestGenericTR181(t *testing.T) {
	params := map[string]string{
		"Device.DeviceInfo.Manufacturer":                        "Global Telecom",
		"Device.DeviceInfo.ManufacturerOUI":                     "00A1B2",
		"Device.DeviceInfo.ProductClass":                        "Titan4000",
		"Device.DeviceInfo.SerialNumber":                        "GT1",
		"Device.DeviceInfo.ModelName":                           "Titan 4000",
		"Device.DeviceInfo.HardwareVersion":                     "A",
		"Device.DeviceInfo.SoftwareVersion":                     "2.0.1",
		"Device.DeviceInfo.UpTime":                              "3600",
		"Device.ManagementServer.URL":                           "https://acs.example/",
		"Device.ManagementServer.Username":                      "00A1B2-Titan4000-GT1",
		"Device.ManagementServer.ConnectionRequestURL":          "http://10.1.1.1:7547/",
		"Device.ManagementServer.PeriodicInformEnable":          "1",
		"Device.ManagementServer.PeriodicInformInterval":        "300",
		"Device.Cellular.Interface.1.CurrentAccessTechnology":   "",
		"Device.Cellular.Interface.2.CurrentAccessTechnology":   "5GNR",
		"Device.Cellular.Interface.10.CurrentAccessTechnology":  "LTE",
		"Device.Cellular.Interface.2.RSRP":                      "-95",
		"Device.Cellular.Interface.2.RSRQ":                      "-11.5",
		"Device.Cellular.Interface.2.SNR":                       "14",
		"Device.Cellular.Interface.2.RSSI":                      "not-a-number",
		"Device.Cellular.Interface.2.Band":                      "n71",
		"Device.Cellular.Interface.2.IMEI":                      "350000000000001",
		"Device.Cellular.Interface.2.USIM.IMSI":                 "310260000000001",
		"Device.Cellular.Interface.2.USIM.ICCID":                "8901260000000000001",
		"Device.Cellular.AccessPoint.1.APN":                     "fwa.carrier",
		"Device.IP.Interface.1.IPv4Address.1.IPAddress":         "127.0.0.1",
		"Device.IP.Interface.2.IPv4Address.1.IPAddress":         "100.64.1.2",
		"Device.IP.Interface.2.IPv6Address.1.IPAddress":         "fe80::1",
		"Device.IP.Interface.2.IPv6Address.2.IPAddress":         "2001:db8::2",
		"Device.IP.Interface.3.IPv4Address.1.IPAddress":         "192.168.1.1",
		"Device.Cellular.Interface.2.X_VENDOR_Unrelated":        "x",
		"Device.Cellular.Interface.2.USIM.X_VENDOR_Unrelated.1": "y",
	}
	h := datamodel.Generic()
	assert.Equal(t, datamodel.GenericHandlerName, h.Name())
	assert.Contains(t, h.RefreshPaths(datamodel.RootTR181), "Device.Cellular.")
	got := h.Normalize(datamodel.RootTR181, params)
	assert.Equal(t, &datamodel.Model{
		Root: datamodel.RootTR181,
		Identity: datamodel.Identity{
			Manufacturer: "Global Telecom", OUI: "00A1B2", ProductClass: "Titan4000", SerialNumber: "GT1",
			ModelName: "Titan 4000", HardwareVersion: "A",
		},
		Firmware:  datamodel.Firmware{SoftwareVersion: "2.0.1"},
		UptimeSec: i(3600),
		Cellular: datamodel.Cellular{
			Technology: "5GNR", Band: "n71", RSRP: f(-95), RSRQ: f(-11.5), SINR: f(14),
			IMEI: "350000000000001", IMSI: "310260000000001", ICCID: "8901260000000000001", APN: "fwa.carrier",
		},
		// The first global address wins: loopback and link-local are skipped.
		WAN: datamodel.WAN{IPv4Address: "100.64.1.2", IPv6Address: "2001:db8::2"},
		ManagementServer: datamodel.ManagementServer{
			URL: "https://acs.example/", Username: "00A1B2-Titan4000-GT1", ConnectionRequestURL: "http://10.1.1.1:7547/",
			PeriodicInformEnable: b(true), PeriodicInformInterval: i(300),
		},
	}, got)
}

func TestGenericTR098(t *testing.T) {
	params := map[string]string{
		"InternetGatewayDevice.DeviceInfo.SoftwareVersion":                                             "9.9",
		"InternetGatewayDevice.ManagementServer.ConnectionRequestURL":                                  "http://1.2.3.4/",
		"InternetGatewayDevice.WANDevice.1.WANConnectionDevice.1.WANIPConnection.1.ExternalIPAddress":  "0.0.0.0",
		"InternetGatewayDevice.WANDevice.1.WANConnectionDevice.1.WANPPPConnection.1.ExternalIPAddress": "203.0.113.9",
		"Device.DeviceInfo.SoftwareVersion":                                                            "ignored under the other root",
	}
	h := datamodel.Generic()
	assert.Contains(t, h.RefreshPaths(datamodel.RootTR098), "InternetGatewayDevice.WANDevice.")
	got := h.Normalize(datamodel.RootTR098, params)
	assert.Equal(t, "9.9", got.Firmware.SoftwareVersion)
	assert.Equal(t, "http://1.2.3.4/", got.ManagementServer.ConnectionRequestURL)
	assert.Equal(t, "203.0.113.9", got.WAN.IPv4Address)
	assert.Nil(t, got.Cellular.RSRP)
	assert.Nil(t, h.RefreshPaths("Unknown."))
}

func TestDetectRoot(t *testing.T) {
	assert.Equal(t, datamodel.RootTR181, datamodel.DetectRoot([]string{"Device.DeviceInfo.SerialNumber"}))
	assert.Equal(t, datamodel.RootTR098, datamodel.DetectRoot([]string{"Device.X", "InternetGatewayDevice.DeviceInfo.SerialNumber"}))
	assert.Equal(t, "", datamodel.DetectRoot([]string{"X.Y"}))
	assert.Equal(t, "", datamodel.DetectRoot(nil))
	assert.Equal(t, "Device.ManagementServer.Password", datamodel.ManagementServerPath("", "Password"))
	assert.Equal(t, "InternetGatewayDevice.ManagementServer.Username", datamodel.ManagementServerPath(datamodel.RootTR098, "Username"))
}

func TestMerge(t *testing.T) {
	m := &datamodel.Model{
		Root:     datamodel.RootTR181,
		Identity: datamodel.Identity{SerialNumber: "S", ModelName: "M"},
		Cellular: datamodel.Cellular{RSRP: f(-100), Band: "n41"},
	}
	m.Merge(&datamodel.Model{Identity: datamodel.Identity{ModelName: "M2"}, Cellular: datamodel.Cellular{RSRP: f(-90)}})
	m.Merge(nil)
	assert.Equal(t, &datamodel.Model{
		Root:     datamodel.RootTR181,
		Identity: datamodel.Identity{SerialNumber: "S", ModelName: "M2"},
		Cellular: datamodel.Cellular{RSRP: f(-90), Band: "n41"},
	}, m)
}

func TestRegistrySelect(t *testing.T) {
	titan := &datamodel.Spec{HandlerName: "titan4000", OUIs: []string{"00a1b2"}, ProductClasses: []string{"Titan4000"}}
	anyGT := &datamodel.Spec{HandlerName: "gt", OUIs: []string{"00A1B2"}}
	byModel := &datamodel.Spec{HandlerName: "titan5400", ModelPattern: regexp.MustCompile(`^Titan ?5400`), Priority: 5}
	r := datamodel.NewRegistry(anyGT, titan, byModel)

	assert.Equal(t, "titan4000", r.Select(datamodel.DeviceInfo{OUI: "00A1B2", ProductClass: "titan4000"}).Name())
	assert.Equal(t, "gt", r.Select(datamodel.DeviceInfo{OUI: "00A1B2", ProductClass: "Other"}).Name())
	assert.Equal(t, "titan5400", r.Select(datamodel.DeviceInfo{OUI: "FFFFFF", ModelName: "Titan 5400"}).Name())
	assert.Equal(t, "generic", r.Select(datamodel.DeviceInfo{OUI: "FFFFFF"}).Name())
	assert.Equal(t, "titan4000", r.Get("titan4000").Name())
	assert.Equal(t, "generic", r.Get("removed").Name())
	assert.Equal(t, datamodel.Quirks{}, r.Get("gt").Quirks())

	info := datamodel.InfoFromParams(datamodel.DeviceInfo{OUI: "O"}, map[string]string{
		"InternetGatewayDevice.DeviceInfo.ModelName":       "Titan 5400",
		"InternetGatewayDevice.DeviceInfo.SoftwareVersion": "1.0",
	})
	assert.Equal(t, datamodel.DeviceInfo{OUI: "O", ModelName: "Titan 5400", SoftwareVersion: "1.0"}, info)
}

// TestInformCaptures checks that every captured Inform yields a handler and
// an identity, so new lab captures exercise the handlers with no code change.
func TestInformCaptures(t *testing.T) {
	sessions, err := capture.LoadDir("../cwmp/testdata/captures")
	require.NoError(t, err)
	r := datamodel.NewRegistry()
	informs := 0
	for _, s := range sessions {
		for _, ex := range s.Exchanges {
			if !ex.IsCWMP() || cwmp.IsEmpty(ex.Request) {
				continue
			}
			env, err := cwmp.Decode(ex.Request)
			require.NoError(t, err)
			inform, ok := env.Body.(*cwmp.Inform)
			if !ok {
				continue
			}
			informs++
			params := inform.ParameterList.Map()
			names := make([]string, 0, len(params))
			for n := range params {
				names = append(names, n)
			}
			root := datamodel.DetectRoot(names)
			require.NotEmpty(t, root, "%s/%s: no known root in the Inform", s.Device, s.ID)
			info := datamodel.InfoFromParams(datamodel.DeviceInfo{
				Manufacturer: inform.DeviceId.Manufacturer, OUI: inform.DeviceId.OUI, ProductClass: inform.DeviceId.ProductClass,
			}, params)
			m := r.Select(info).Normalize(root, params)
			assert.NotEmpty(t, m.Firmware.SoftwareVersion, "%s/%s", s.Device, s.ID)
		}
	}
	assert.NotZero(t, informs)
}
