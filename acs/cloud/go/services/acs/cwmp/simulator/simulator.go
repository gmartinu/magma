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

// Package simulator is a small CWMP client that behaves like a CPE, used to
// test the ACS end to end without hardware. It follows the lab simulator in
// tools/acs-lab/sim.
package simulator

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"sort"
	"strings"
	"time"

	"magma/acs/cloud/go/services/acs/auth"
	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/datamodel"
)

// Param is a parameter of the simulated data model.
type Param struct {
	Value    string
	Type     string
	Writable bool
}

// CPE is a simulated CPE. It is not safe for concurrent sessions.
type CPE struct {
	URL      string
	Root     string
	DeviceID cwmp.DeviceIDStruct
	// Namespace is the cwmp namespace the CPE speaks.
	Namespace string
	Username  string
	Password  string
	// PreferBasic answers with Basic when the ACS offers it.
	PreferBasic bool
	Params      map[string]Param
	// InformParams are the parameters sent in every Inform.
	InformParams []string
	// Faults makes the CPE answer an RPC method with a fault instead.
	Faults map[string]*cwmp.Fault
	// StopAfter makes the CPE drop the session after answering this many ACS
	// requests, like a CPE that loses its connection; 0 never stops.
	StopAfter int
	// ApplyAndDrop makes the CPE apply a request of this method and then
	// drop the session before answering, as when the answer is lost.
	ApplyAndDrop string
	// Headers are added to every request, e.g. the X-Forwarded-* a proxy
	// would set.
	Headers map[string]string

	client        *http.Client
	bootstrapUser string
	bootstrapPass string
	pendingEvents []cwmp.EventStruct
	challenge     *auth.ClientChallenge
	nc            int
}

// Session is what happened in one session.
type Session struct {
	// Requests are the ACS requests the CPE received, in order.
	Requests []cwmp.Message
	// Status is the HTTP status that ended the session: 204 when the ACS ended
	// it, or the error status.
	Status int
	// Namespace is the cwmp namespace of the InformResponse.
	Namespace string
}

// New returns a CPE with a default data model under root that authenticates
// with username and password, which it treats as its factory credentials.
func New(acsURL, root string, id cwmp.DeviceIDStruct, username, password string) *CPE {
	c := &CPE{
		URL:           acsURL,
		Root:          root,
		DeviceID:      id,
		Namespace:     cwmp.NSCwmp10,
		Username:      username,
		Password:      password,
		bootstrapUser: username,
		bootstrapPass: password,
		Faults:        map[string]*cwmp.Fault{},
		Headers:       map[string]string{},
	}
	c.factoryDefaults()
	jar, _ := cookiejar.New(nil)
	c.client = &http.Client{Jar: jar, Timeout: 10 * time.Second}
	return c
}

// DisableCookies makes the CPE drop the session cookie, as some stacks do.
func (c *CPE) DisableCookies() {
	c.client.Jar = nil
}

// SetTransport routes the CPE's requests, e.g. across several ACS replicas.
func (c *CPE) SetTransport(rt http.RoundTripper) {
	c.client.Transport = rt
}

func (c *CPE) factoryDefaults() {
	r := c.Root
	p := map[string]Param{}
	add := func(name, value, typ string, writable bool) {
		p[r+name] = Param{Value: value, Type: typ, Writable: writable}
	}
	add("DeviceInfo.Manufacturer", c.DeviceID.Manufacturer, "xsd:string", false)
	add("DeviceInfo.ManufacturerOUI", c.DeviceID.OUI, "xsd:string", false)
	add("DeviceInfo.ProductClass", c.DeviceID.ProductClass, "xsd:string", false)
	add("DeviceInfo.SerialNumber", c.DeviceID.SerialNumber, "xsd:string", false)
	add("DeviceInfo.ModelName", c.DeviceID.ProductClass, "xsd:string", false)
	add("DeviceInfo.HardwareVersion", "HW-A", "xsd:string", false)
	add("DeviceInfo.SoftwareVersion", "1.0.0-sim", "xsd:string", false)
	add("DeviceInfo.UpTime", "100", "xsd:unsignedInt", false)
	add("ManagementServer.URL", c.URL, "xsd:string", true)
	add("ManagementServer.Username", c.bootstrapUser, "xsd:string", true)
	add("ManagementServer.Password", "", "xsd:string", true)
	add("ManagementServer.PeriodicInformEnable", "1", "xsd:boolean", true)
	add("ManagementServer.PeriodicInformInterval", "300", "xsd:unsignedInt", true)
	add("ManagementServer.ConnectionRequestURL", "http://192.0.2.10:7548/cr", "xsd:string", false)
	add("ManagementServer.ConnectionRequestUsername", "", "xsd:string", true)
	add("ManagementServer.ConnectionRequestPassword", "", "xsd:string", true)
	add("ManagementServer.ParameterKey", "", "xsd:string", false)
	if r == datamodel.RootTR098 {
		add("WANDevice.1.WANConnectionDevice.1.WANIPConnection.1.ExternalIPAddress", "100.64.0.9", "xsd:string", false)
	} else {
		add("Cellular.Interface.1.CurrentAccessTechnology", "5GNR", "xsd:string", false)
		add("Cellular.Interface.1.RSRP", "-95", "xsd:int", false)
		add("Cellular.Interface.1.RSRQ", "-11", "xsd:int", false)
		add("Cellular.Interface.1.SINR", "14", "xsd:int", false)
		add("Cellular.Interface.1.Band", "n71", "xsd:string", false)
		add("Cellular.Interface.1.IMEI", "350000000000001", "xsd:string", false)
		add("Cellular.AccessPoint.1.APN", "fwa.sim", "xsd:string", true)
		add("IP.Interface.1.IPv4Address.1.IPAddress", "127.0.0.1", "xsd:string", false)
		add("IP.Interface.2.IPv4Address.1.IPAddress", "100.64.0.9", "xsd:string", false)
	}
	c.Params = p
	c.InformParams = []string{
		r + "DeviceInfo.Manufacturer", r + "DeviceInfo.ManufacturerOUI", r + "DeviceInfo.ProductClass",
		r + "DeviceInfo.SerialNumber", r + "DeviceInfo.HardwareVersion", r + "DeviceInfo.SoftwareVersion",
		r + "ManagementServer.ConnectionRequestURL", r + "ManagementServer.ParameterKey",
	}
}

// RunSession opens a session with an Inform carrying the events (plus the
// ones left over from a reboot or factory reset) and answers the ACS until
// it ends the session.
func (c *CPE) RunSession(events ...string) (*Session, error) {
	c.challenge, c.nc = nil, 0
	ev := c.pendingEvents
	c.pendingEvents = nil
	for _, e := range events {
		ev = append(ev, cwmp.EventStruct{EventCode: e})
	}
	inform := &cwmp.Inform{
		DeviceId:     c.DeviceID,
		Event:        ev,
		MaxEnvelopes: 1,
		CurrentTime:  time.Now().UTC().Format(time.RFC3339),
	}
	for _, n := range c.InformParams {
		if p, ok := c.Params[n]; ok {
			inform.ParameterList = append(inform.ParameterList, cwmp.ParameterValueStruct{Name: n, Value: p.Value, Type: p.Type})
		}
	}

	sess := &Session{}
	status, body, err := c.post(inform, "inform")
	if err != nil {
		return sess, err
	}
	if status != http.StatusOK {
		sess.Status = status
		return sess, fmt.Errorf("inform: HTTP %d", status)
	}
	env, err := cwmp.Decode(body)
	if err != nil {
		return sess, err
	}
	if _, ok := env.Body.(*cwmp.InformResponse); !ok {
		return sess, fmt.Errorf("inform answered with %s", env.Body.Method())
	}
	sess.Namespace = env.Namespace

	var answer cwmp.Message
	answerID := ""
	for answered := 0; ; answered++ {
		status, body, err = c.post(answer, answerID)
		if err != nil {
			return sess, err
		}
		if status == http.StatusNoContent || (status == http.StatusOK && cwmp.IsEmpty(body)) {
			sess.Status = http.StatusNoContent
			return sess, nil
		}
		if status != http.StatusOK {
			sess.Status = status
			return sess, fmt.Errorf("HTTP %d", status)
		}
		env, err := cwmp.Decode(body)
		if err != nil {
			return sess, err
		}
		sess.Requests = append(sess.Requests, env.Body)
		if c.StopAfter > 0 && answered >= c.StopAfter {
			return sess, nil
		}
		answer, answerID = c.handle(env.Body), env.ID
		if env.Body.Method() == c.ApplyAndDrop {
			return sess, nil
		}
	}
}

// post sends a message (nil for an empty POST), answering one auth challenge.
func (c *CPE) post(msg cwmp.Message, id string) (int, []byte, error) {
	var body []byte
	if msg != nil {
		var err error
		if body, err = cwmp.Encode(&cwmp.Envelope{ID: id, Namespace: c.Namespace, Body: msg}); err != nil {
			return 0, nil, err
		}
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequest(http.MethodPost, c.URL, bytes.NewReader(body))
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Content-Type", "text/xml; charset=utf-8")
		for k, v := range c.Headers {
			req.Header.Set(k, v)
		}
		if c.challenge != nil {
			req.Header.Set("Authorization", c.authorization(req.URL))
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return 0, nil, err
		}
		rb, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return 0, nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || attempt > 0 {
			return resp.StatusCode, rb, nil
		}
		c.pickChallenge(auth.ParseChallenges(resp.Header.Values("WWW-Authenticate")))
		if c.challenge == nil {
			return resp.StatusCode, rb, nil
		}
	}
}

func (c *CPE) pickChallenge(chs []auth.ClientChallenge) {
	c.challenge = nil
	for i := range chs {
		ch := chs[i]
		if ch.Scheme == "basic" && c.PreferBasic {
			c.challenge = &ch
			return
		}
		if ch.Scheme == "digest" && c.challenge == nil {
			c.challenge = &ch
		}
	}
	c.nc = 0
}

func (c *CPE) authorization(u *url.URL) string {
	if c.challenge.Scheme == "basic" {
		return auth.BasicAuthorization(c.Username, c.Password)
	}
	c.nc++
	return auth.DigestAuthorization(*c.challenge, http.MethodPost, u.RequestURI(), c.Username, c.Password,
		fmt.Sprintf("%08x", c.nc), fmt.Sprintf("sim%d", c.nc))
}

// handle answers one ACS request.
func (c *CPE) handle(m cwmp.Message) cwmp.Message {
	if f, ok := c.Faults[m.Method()]; ok {
		return f
	}
	switch req := m.(type) {
	case *cwmp.GetParameterValues:
		var out cwmp.ParameterValueList
		for _, name := range req.ParameterNames {
			matched := c.match(name, false)
			if len(matched) == 0 {
				return cwmp.NewFault(cwmp.FaultCPEInvalidParamName, "Invalid parameter name "+name)
			}
			for _, n := range matched {
				p := c.Params[n]
				out = append(out, cwmp.ParameterValueStruct{Name: n, Value: p.Value, Type: p.Type})
			}
		}
		return &cwmp.GetParameterValuesResponse{ParameterList: out}
	case *cwmp.GetParameterNames:
		matched := c.match(req.ParameterPath, req.NextLevel)
		if len(matched) == 0 {
			return cwmp.NewFault(cwmp.FaultCPEInvalidParamName, "Invalid parameter name "+req.ParameterPath)
		}
		var out cwmp.ParameterInfoList
		for _, n := range matched {
			out = append(out, cwmp.ParameterInfoStruct{Name: n, Writable: c.Params[n].Writable})
		}
		return &cwmp.GetParameterNamesResponse{ParameterList: out}
	case *cwmp.SetParameterValues:
		var faults []cwmp.SetParameterValuesFault
		for _, p := range req.ParameterList {
			cur, ok := c.Params[p.Name]
			switch {
			case !ok:
				faults = append(faults, cwmp.SetParameterValuesFault{ParameterName: p.Name, FaultCode: cwmp.FaultCPEInvalidParamName, FaultString: "Invalid parameter name"})
			case !cur.Writable:
				faults = append(faults, cwmp.SetParameterValuesFault{ParameterName: p.Name, FaultCode: cwmp.FaultCPENonWritableParam, FaultString: "Attempt to set a non-writable parameter"})
			}
		}
		if len(faults) > 0 {
			f := cwmp.NewFault(cwmp.FaultCPEInvalidArguments, "Invalid arguments")
			f.Detail.SetParameterValuesFault = faults
			return f
		}
		for _, p := range req.ParameterList {
			cur := c.Params[p.Name]
			cur.Value = p.Value
			c.Params[p.Name] = cur
			switch strings.TrimPrefix(p.Name, c.Root) {
			case "ManagementServer.Username":
				c.Username = p.Value
			case "ManagementServer.Password":
				c.Password = p.Value
			}
		}
		key := c.Params[c.Root+"ManagementServer.ParameterKey"]
		key.Value = req.ParameterKey
		c.Params[c.Root+"ManagementServer.ParameterKey"] = key
		return &cwmp.SetParameterValuesResponse{Status: 0}
	case *cwmp.Reboot:
		c.pendingEvents = []cwmp.EventStruct{{EventCode: cwmp.EventBoot}, {EventCode: cwmp.EventMReboot, CommandKey: req.CommandKey}}
		return &cwmp.RebootResponse{}
	case *cwmp.FactoryReset:
		c.Username, c.Password = c.bootstrapUser, c.bootstrapPass
		c.factoryDefaults()
		c.pendingEvents = []cwmp.EventStruct{{EventCode: cwmp.EventBootstrap}, {EventCode: cwmp.EventBoot}}
		return &cwmp.FactoryResetResponse{}
	}
	return cwmp.NewFault(cwmp.FaultCPEMethodNotSupported, "Method not supported")
}

// match returns the parameter names under a partial path, or the name itself
// if it is a parameter. With nextLevel only the direct children are listed.
func (c *CPE) match(path string, nextLevel bool) []string {
	if path == "" {
		path = c.Root
	}
	if !strings.HasSuffix(path, ".") {
		if _, ok := c.Params[path]; ok {
			return []string{path}
		}
		return nil
	}
	seen := map[string]bool{}
	for n := range c.Params {
		if !strings.HasPrefix(n, path) {
			continue
		}
		if nextLevel {
			rest := strings.TrimPrefix(n, path)
			if i := strings.IndexByte(rest, '.'); i >= 0 {
				n = path + rest[:i+1]
			}
		}
		seen[n] = true
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
