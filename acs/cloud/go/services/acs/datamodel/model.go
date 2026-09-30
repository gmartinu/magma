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

// Package datamodel maps vendor TR-069 parameters onto a normalized CPE model
// through per-model handlers.
package datamodel

import (
	"encoding/json"
	"strings"
)

const (
	// RootTR181 is the data model root of TR-181 (Device:2) CPEs.
	RootTR181 = "Device."
	// RootTR098 is the data model root of TR-098 (InternetGatewayDevice:1).
	RootTR098 = "InternetGatewayDevice."
)

// Model is the vendor-independent view of a CPE. Fields the CPE did not
// report are left empty, so a partial model can be merged onto a fuller one.
type Model struct {
	Root             string           `json:"root,omitempty"`
	Identity         Identity         `json:"identity,omitempty"`
	Firmware         Firmware         `json:"firmware,omitempty"`
	UptimeSec        *int64           `json:"uptime_sec,omitempty"`
	Cellular         Cellular         `json:"cellular,omitempty"`
	WAN              WAN              `json:"wan,omitempty"`
	ManagementServer ManagementServer `json:"management_server,omitempty"`
}

type Identity struct {
	Manufacturer    string `json:"manufacturer,omitempty"`
	OUI             string `json:"oui,omitempty"`
	ProductClass    string `json:"product_class,omitempty"`
	SerialNumber    string `json:"serial_number,omitempty"`
	ModelName       string `json:"model_name,omitempty"`
	HardwareVersion string `json:"hardware_version,omitempty"`
}

type Firmware struct {
	SoftwareVersion string `json:"software_version,omitempty"`
}

// Cellular holds the radio KPIs of the WAN modem. Signal values are in dB or
// dBm as the CPE reports them.
type Cellular struct {
	Technology string   `json:"technology,omitempty"`
	Band       string   `json:"band,omitempty"`
	CellID     string   `json:"cell_id,omitempty"`
	PCI        string   `json:"pci,omitempty"`
	RSRP       *float64 `json:"rsrp,omitempty"`
	RSRQ       *float64 `json:"rsrq,omitempty"`
	SINR       *float64 `json:"sinr,omitempty"`
	RSSI       *float64 `json:"rssi,omitempty"`
	IMEI       string   `json:"imei,omitempty"`
	IMSI       string   `json:"imsi,omitempty"`
	ICCID      string   `json:"iccid,omitempty"`
	APN        string   `json:"apn,omitempty"`
	Operator   string   `json:"operator,omitempty"`
}

type WAN struct {
	IPv4Address string `json:"ipv4_address,omitempty"`
	IPv6Address string `json:"ipv6_address,omitempty"`
}

type ManagementServer struct {
	URL                    string `json:"url,omitempty"`
	Username               string `json:"username,omitempty"`
	ConnectionRequestURL   string `json:"connection_request_url,omitempty"`
	PeriodicInformEnable   *bool  `json:"periodic_inform_enable,omitempty"`
	PeriodicInformInterval *int64 `json:"periodic_inform_interval,omitempty"`
}

// Merge overwrites m with every field that o sets.
func (m *Model) Merge(o *Model) {
	if o == nil {
		return
	}
	// omitempty drops the unset fields of o, so unmarshaling it onto m only
	// touches what o reports.
	b, err := json.Marshal(o)
	if err == nil {
		_ = json.Unmarshal(b, m)
	}
}

// DetectRoot returns the data model root used by a set of parameter names,
// or "" if none of them is under a known root.
func DetectRoot(names []string) string {
	root := ""
	for _, n := range names {
		switch {
		case strings.HasPrefix(n, RootTR098):
			return RootTR098
		case strings.HasPrefix(n, RootTR181):
			root = RootTR181
		}
	}
	return root
}
