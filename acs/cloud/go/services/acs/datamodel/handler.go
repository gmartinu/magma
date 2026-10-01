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

import (
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// DeviceInfo is what a CPE says about itself in an Inform, used to pick its
// handler.
type DeviceInfo struct {
	Manufacturer    string
	OUI             string
	ProductClass    string
	ModelName       string
	SoftwareVersion string
}

// Quirks are the per-model deviations the ACS core has to honor.
type Quirks struct {
	// MaxGPVNames caps the parameter names per GetParameterValues; 0 means no
	// cap.
	MaxGPVNames int
	// PasswordLength is the length of the ACS credentials generated for the
	// CPE on rotation; 0 means DefaultPasswordLength.
	PasswordLength int
	// NoCredentialRotation keeps the bootstrap credentials on CPEs that do not
	// accept a new ManagementServer.Password.
	NoCredentialRotation bool
}

// DefaultPasswordLength fits the 256-character limit of TR-069 with room for
// CPEs that store less.
const DefaultPasswordLength = 32

// Handler maps one family of CPEs onto the normalized model.
type Handler interface {
	Name() string
	// Match scores how well the handler fits the device; 0 means not at all.
	Match(info DeviceInfo) int
	// RefreshPaths are the parameter names or partial paths (ending in ".")
	// read to refresh the model. Each one is read in its own
	// GetParameterValues, so a path the CPE does not have fails alone.
	RefreshPaths(root string) []string
	// Normalize maps the parameters of the CPE onto the model.
	Normalize(root string, params map[string]string) *Model
	Quirks() Quirks
}

// Field is a settable field of Model.
type Field string

const (
	FieldManufacturer           Field = "identity.manufacturer"
	FieldOUI                    Field = "identity.oui"
	FieldProductClass           Field = "identity.product_class"
	FieldSerialNumber           Field = "identity.serial_number"
	FieldModelName              Field = "identity.model_name"
	FieldHardwareVersion        Field = "identity.hardware_version"
	FieldSoftwareVersion        Field = "firmware.software_version"
	FieldUptime                 Field = "uptime_sec"
	FieldTechnology             Field = "cellular.technology"
	FieldBand                   Field = "cellular.band"
	FieldCellID                 Field = "cellular.cell_id"
	FieldPCI                    Field = "cellular.pci"
	FieldRSRP                   Field = "cellular.rsrp"
	FieldRSRQ                   Field = "cellular.rsrq"
	FieldSINR                   Field = "cellular.sinr"
	FieldRSSI                   Field = "cellular.rssi"
	FieldIMEI                   Field = "cellular.imei"
	FieldIMSI                   Field = "cellular.imsi"
	FieldICCID                  Field = "cellular.iccid"
	FieldAPN                    Field = "cellular.apn"
	FieldOperator               Field = "cellular.operator"
	FieldWANIPv4                Field = "wan.ipv4_address"
	FieldWANIPv6                Field = "wan.ipv6_address"
	FieldMSURL                  Field = "management_server.url"
	FieldMSUsername             Field = "management_server.username"
	FieldMSConnReqURL           Field = "management_server.connection_request_url"
	FieldMSPeriodicInformEnable Field = "management_server.periodic_inform_enable"
	FieldMSPeriodicInformIntvl  Field = "management_server.periodic_inform_interval"
)

// setters apply a raw value to a field and report whether it was accepted.
var setters = map[Field]func(m *Model, v string) bool{
	FieldManufacturer:           str(func(m *Model) *string { return &m.Identity.Manufacturer }),
	FieldOUI:                    str(func(m *Model) *string { return &m.Identity.OUI }),
	FieldProductClass:           str(func(m *Model) *string { return &m.Identity.ProductClass }),
	FieldSerialNumber:           str(func(m *Model) *string { return &m.Identity.SerialNumber }),
	FieldModelName:              str(func(m *Model) *string { return &m.Identity.ModelName }),
	FieldHardwareVersion:        str(func(m *Model) *string { return &m.Identity.HardwareVersion }),
	FieldSoftwareVersion:        str(func(m *Model) *string { return &m.Firmware.SoftwareVersion }),
	FieldUptime:                 integer(func(m *Model) **int64 { return &m.UptimeSec }),
	FieldTechnology:             str(func(m *Model) *string { return &m.Cellular.Technology }),
	FieldBand:                   str(func(m *Model) *string { return &m.Cellular.Band }),
	FieldCellID:                 str(func(m *Model) *string { return &m.Cellular.CellID }),
	FieldPCI:                    str(func(m *Model) *string { return &m.Cellular.PCI }),
	FieldRSRP:                   float(func(m *Model) **float64 { return &m.Cellular.RSRP }),
	FieldRSRQ:                   float(func(m *Model) **float64 { return &m.Cellular.RSRQ }),
	FieldSINR:                   float(func(m *Model) **float64 { return &m.Cellular.SINR }),
	FieldRSSI:                   float(func(m *Model) **float64 { return &m.Cellular.RSSI }),
	FieldIMEI:                   str(func(m *Model) *string { return &m.Cellular.IMEI }),
	FieldIMSI:                   str(func(m *Model) *string { return &m.Cellular.IMSI }),
	FieldICCID:                  str(func(m *Model) *string { return &m.Cellular.ICCID }),
	FieldAPN:                    str(func(m *Model) *string { return &m.Cellular.APN }),
	FieldOperator:               str(func(m *Model) *string { return &m.Cellular.Operator }),
	FieldWANIPv4:                ip(false, func(m *Model) *string { return &m.WAN.IPv4Address }),
	FieldWANIPv6:                ip(true, func(m *Model) *string { return &m.WAN.IPv6Address }),
	FieldMSURL:                  str(func(m *Model) *string { return &m.ManagementServer.URL }),
	FieldMSUsername:             str(func(m *Model) *string { return &m.ManagementServer.Username }),
	FieldMSConnReqURL:           str(func(m *Model) *string { return &m.ManagementServer.ConnectionRequestURL }),
	FieldMSPeriodicInformEnable: boolean(func(m *Model) **bool { return &m.ManagementServer.PeriodicInformEnable }),
	FieldMSPeriodicInformIntvl:  integer(func(m *Model) **int64 { return &m.ManagementServer.PeriodicInformInterval }),
}

func str(f func(*Model) *string) func(*Model, string) bool {
	return func(m *Model, v string) bool {
		*f(m) = v
		return true
	}
}

func integer(f func(*Model) **int64) func(*Model, string) bool {
	return func(m *Model, v string) bool {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return false
		}
		*f(m) = &n
		return true
	}
}

func float(f func(*Model) **float64) func(*Model, string) bool {
	return func(m *Model, v string) bool {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return false
		}
		*f(m) = &n
		return true
	}
}

func boolean(f func(*Model) **bool) func(*Model, string) bool {
	return func(m *Model, v string) bool {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return false
		}
		*f(m) = &b
		return true
	}
}

// ip accepts only a global address of the family, since the CPE lists its
// loopback and LAN interfaces next to the WAN one.
func ip(v6 bool, f func(*Model) *string) func(*Model, string) bool {
	return func(m *Model, v string) bool {
		a := net.ParseIP(v)
		if a == nil || (a.To4() == nil) != v6 || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
			return false
		}
		*f(m) = v
		return true
	}
}

// Spec is a table-driven Handler. Parameter names in Fields may contain "{i}",
// which matches any instance number; the lowest instances are tried first.
type Spec struct {
	HandlerName string
	// OUIs, ProductClasses and ModelPattern restrict the devices the spec
	// matches; an empty criterion matches anything.
	OUIs           []string
	ProductClasses []string
	ModelPattern   *regexp.Regexp
	// Priority breaks ties between specs that match equally well.
	Priority int
	// Refresh and Fields are keyed by data model root.
	Refresh map[string][]string
	Fields  map[string]map[Field][]string
	Quirk   Quirks
}

func (s *Spec) Name() string { return s.HandlerName }

func (s *Spec) Quirks() Quirks { return s.Quirk }

func (s *Spec) Match(info DeviceInfo) int {
	score := 1 + s.Priority
	if len(s.OUIs) > 0 {
		if !containsFold(s.OUIs, info.OUI) {
			return 0
		}
		score += 10
	}
	if len(s.ProductClasses) > 0 {
		if !containsFold(s.ProductClasses, info.ProductClass) {
			return 0
		}
		score += 10
	}
	if s.ModelPattern != nil {
		if !s.ModelPattern.MatchString(info.ModelName) && !s.ModelPattern.MatchString(info.ProductClass) {
			return 0
		}
		score += 10
	}
	return score
}

func (s *Spec) RefreshPaths(root string) []string {
	return s.Refresh[root]
}

func (s *Spec) Normalize(root string, params map[string]string) *Model {
	m := &Model{Root: root}
	var names []string
	for field, candidates := range s.Fields[root] {
		set := setters[field]
		if set == nil {
			continue
		}
	candidateLoop:
		for _, c := range candidates {
			if !strings.Contains(c, "{i}") {
				if v := strings.TrimSpace(params[c]); v != "" && set(m, v) {
					break
				}
				continue
			}
			if names == nil {
				names = sortedNames(params)
			}
			re := instancePattern(c)
			for _, n := range names {
				if v := strings.TrimSpace(params[n]); v != "" && re.MatchString(n) && set(m, v) {
					break candidateLoop
				}
			}
		}
	}
	return m
}

var instanceRes sync.Map

func instancePattern(path string) *regexp.Regexp {
	if re, ok := instanceRes.Load(path); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(path), `\{i\}`, `[0-9]+`) + "$")
	instanceRes.Store(path, re)
	return re
}

// sortedNames orders names so that lower instance numbers come first, e.g.
// Interface.2 before Interface.10.
func sortedNames(params map[string]string) []string {
	names := make([]string, 0, len(params))
	for n := range params {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool { return lessNatural(names[i], names[j]) })
	return names
}

func lessNatural(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			return an < bn
		}
		return as[i] < bs[i]
	}
	return len(as) < len(bs)
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// Registry selects the handler of a device.
type Registry struct {
	handlers []Handler
	fallback Handler
}

// NewRegistry returns a registry of the handlers with the generic handler as
// the fallback.
func NewRegistry(handlers ...Handler) *Registry {
	return &Registry{handlers: handlers, fallback: Generic()}
}

// Select returns the best-matching handler, or the generic one. Ties go to
// the handler registered first.
func (r *Registry) Select(info DeviceInfo) Handler {
	best, bestScore := r.fallback, 0
	for _, h := range r.handlers {
		if score := h.Match(info); score > bestScore {
			best, bestScore = h, score
		}
	}
	return best
}

// Get returns a handler by name, or the generic one if it is unknown, e.g.
// after the handler of a device was removed.
func (r *Registry) Get(name string) Handler {
	for _, h := range r.handlers {
		if h.Name() == name {
			return h
		}
	}
	return r.fallback
}

// InfoFromParams completes the Inform DeviceId with the model and firmware
// the CPE reports in its Inform parameters.
func InfoFromParams(info DeviceInfo, params map[string]string) DeviceInfo {
	for _, root := range []string{RootTR181, RootTR098} {
		if v := params[root+"DeviceInfo.ModelName"]; v != "" && info.ModelName == "" {
			info.ModelName = v
		}
		if v := params[root+"DeviceInfo.SoftwareVersion"]; v != "" && info.SoftwareVersion == "" {
			info.SoftwareVersion = v
		}
	}
	return info
}
