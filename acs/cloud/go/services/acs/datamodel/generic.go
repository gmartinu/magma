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

package datamodel

// GenericHandlerName is the handler of CPEs no model handler claims.
const GenericHandlerName = "generic"

// Generic returns the default handler. It reads the standard TR-181 and
// TR-098 objects; the cellular names it tries beyond the TR-181
// Device.Cellular object are common placeholders that model handlers are
// expected to override.
func Generic() Handler {
	return genericSpec
}

var genericSpec = &Spec{
	HandlerName: GenericHandlerName,
	Refresh: map[string][]string{
		RootTR181: {
			"Device.DeviceInfo.",
			"Device.ManagementServer.",
			"Device.Cellular.",
			"Device.IP.Interface.",
		},
		RootTR098: {
			"InternetGatewayDevice.DeviceInfo.",
			"InternetGatewayDevice.ManagementServer.",
			"InternetGatewayDevice.WANDevice.",
		},
	},
	Fields: map[string]map[Field][]string{
		RootTR181: withCommon(RootTR181, map[Field][]string{
			FieldTechnology: {"Device.Cellular.Interface.{i}.CurrentAccessTechnology"},
			FieldIMEI:       {"Device.Cellular.Interface.{i}.IMEI"},
			FieldIMSI:       {"Device.Cellular.Interface.{i}.USIM.IMSI"},
			FieldICCID:      {"Device.Cellular.Interface.{i}.USIM.ICCID"},
			FieldOperator:   {"Device.Cellular.Interface.{i}.NetworkInUse"},
			FieldAPN:        {"Device.Cellular.AccessPoint.{i}.APN"},
			FieldRSRP:       {"Device.Cellular.Interface.{i}.RSRP"},
			FieldRSRQ:       {"Device.Cellular.Interface.{i}.RSRQ"},
			FieldRSSI:       {"Device.Cellular.Interface.{i}.RSSI"},
			FieldSINR:       {"Device.Cellular.Interface.{i}.SINR", "Device.Cellular.Interface.{i}.SNR"},
			FieldBand:       {"Device.Cellular.Interface.{i}.Band", "Device.Cellular.Interface.{i}.CurrentBand"},
			FieldCellID:     {"Device.Cellular.Interface.{i}.CellID", "Device.Cellular.Interface.{i}.CellId"},
			FieldPCI:        {"Device.Cellular.Interface.{i}.PCI", "Device.Cellular.Interface.{i}.PhysicalCellID"},
			FieldWANIPv4:    {"Device.IP.Interface.{i}.IPv4Address.{i}.IPAddress"},
			FieldWANIPv6:    {"Device.IP.Interface.{i}.IPv6Address.{i}.IPAddress"},
		}),
		RootTR098: withCommon(RootTR098, map[Field][]string{
			FieldWANIPv4: {
				"InternetGatewayDevice.WANDevice.{i}.WANConnectionDevice.{i}.WANIPConnection.{i}.ExternalIPAddress",
				"InternetGatewayDevice.WANDevice.{i}.WANConnectionDevice.{i}.WANPPPConnection.{i}.ExternalIPAddress",
			},
		}),
	},
}

// withCommon adds the DeviceInfo and ManagementServer fields, which TR-181
// and TR-098 name the same way under their roots.
func withCommon(root string, fields map[Field][]string) map[Field][]string {
	common := map[Field]string{
		FieldManufacturer:           "DeviceInfo.Manufacturer",
		FieldOUI:                    "DeviceInfo.ManufacturerOUI",
		FieldProductClass:           "DeviceInfo.ProductClass",
		FieldSerialNumber:           "DeviceInfo.SerialNumber",
		FieldModelName:              "DeviceInfo.ModelName",
		FieldHardwareVersion:        "DeviceInfo.HardwareVersion",
		FieldSoftwareVersion:        "DeviceInfo.SoftwareVersion",
		FieldUptime:                 "DeviceInfo.UpTime",
		FieldMSURL:                  "ManagementServer.URL",
		FieldMSUsername:             "ManagementServer.Username",
		FieldMSConnReqURL:           "ManagementServer.ConnectionRequestURL",
		FieldMSPeriodicInformEnable: "ManagementServer.PeriodicInformEnable",
		FieldMSPeriodicInformIntvl:  "ManagementServer.PeriodicInformInterval",
	}
	for f, p := range common {
		fields[f] = []string{root + p}
	}
	return fields
}

// ManagementServerPath returns a ManagementServer parameter under the root,
// e.g. "Device.ManagementServer.Password".
func ManagementServerPath(root, param string) string {
	if root == "" {
		root = RootTR181
	}
	return root + "ManagementServer." + param
}
