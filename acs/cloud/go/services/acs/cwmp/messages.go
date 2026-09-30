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

package cwmp

import (
	"encoding/xml"
)

// Message is the body of a CWMP envelope: an RPC, an RPC response or a fault.
type Message interface {
	// Method is the local name of the body element, e.g. "Inform".
	Method() string
}

// Event codes of TR-069 Table 7.
const (
	EventBootstrap         = "0 BOOTSTRAP"
	EventBoot              = "1 BOOT"
	EventPeriodic          = "2 PERIODIC"
	EventScheduled         = "3 SCHEDULED"
	EventValueChange       = "4 VALUE CHANGE"
	EventConnectionRequest = "6 CONNECTION REQUEST"
	EventTransferComplete  = "7 TRANSFER COMPLETE"
	EventMReboot           = "M Reboot"
)

type DeviceIDStruct struct {
	Manufacturer string `xml:"Manufacturer"`
	OUI          string `xml:"OUI"`
	ProductClass string `xml:"ProductClass"`
	SerialNumber string `xml:"SerialNumber"`
}

// DeviceID returns the OUI-ProductClass-SerialNumber triplet that identifies
// the CPE in the ACS. ProductClass is optional in TR-069, so it is dropped when
// empty.
func (d DeviceIDStruct) DeviceID() string {
	if d.ProductClass == "" {
		return d.OUI + "-" + d.SerialNumber
	}
	return d.OUI + "-" + d.ProductClass + "-" + d.SerialNumber
}

type EventStruct struct {
	EventCode  string `xml:"EventCode"`
	CommandKey string `xml:"CommandKey"`
}

type EventList []EventStruct

func (l EventList) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return marshalArray(e, start, "EventStruct", "cwmp:EventStruct", l)
}

func (l *EventList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return unmarshalArray(d, (*[]EventStruct)(l))
}

// ParameterValueStruct is a parameter and its value. Type is the xsi:type of
// the value as sent on the wire, e.g. "xsd:string".
type ParameterValueStruct struct {
	Name  string
	Value string
	Type  string
}

func (p ParameterValueStruct) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	typ := p.Type
	if typ == "" {
		typ = "xsd:string"
	}
	err := e.EncodeToken(start)
	if err == nil {
		err = e.EncodeElement(p.Name, xml.StartElement{Name: xml.Name{Local: "Name"}})
	}
	if err == nil {
		err = e.EncodeElement(p.Value, xml.StartElement{
			Name: xml.Name{Local: "Value"},
			Attr: []xml.Attr{{Name: xml.Name{Local: "xsi:type"}, Value: typ}},
		})
	}
	if err == nil {
		err = e.EncodeToken(start.End())
	}
	return err
}

func (p *ParameterValueStruct) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var aux struct {
		Name  string `xml:"Name"`
		Value struct {
			Type  string `xml:"type,attr"`
			Value string `xml:",chardata"`
		} `xml:"Value"`
	}
	if err := d.DecodeElement(&aux, &start); err != nil {
		return err
	}
	*p = ParameterValueStruct{Name: aux.Name, Value: aux.Value.Value, Type: aux.Value.Type}
	return nil
}

type ParameterValueList []ParameterValueStruct

func (l ParameterValueList) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return marshalArray(e, start, "ParameterValueStruct", "cwmp:ParameterValueStruct", l)
}

func (l *ParameterValueList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return unmarshalArray(d, (*[]ParameterValueStruct)(l))
}

// Map returns the parameters by name.
func (l ParameterValueList) Map() map[string]string {
	m := make(map[string]string, len(l))
	for _, p := range l {
		m[p.Name] = p.Value
	}
	return m
}

type ParameterInfoStruct struct {
	Name     string `xml:"Name"`
	Writable bool   `xml:"Writable"`
}

type ParameterInfoList []ParameterInfoStruct

func (l ParameterInfoList) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return marshalArray(e, start, "ParameterInfoStruct", "cwmp:ParameterInfoStruct", l)
}

func (l *ParameterInfoList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return unmarshalArray(d, (*[]ParameterInfoStruct)(l))
}

// StringList is an xsd:string array such as ParameterNames or MethodList.
type StringList []string

func (l StringList) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	return marshalArray(e, start, "string", "xsd:string", l)
}

func (l *StringList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	return unmarshalArray(d, (*[]string)(l))
}

type Inform struct {
	DeviceId      DeviceIDStruct     `xml:"DeviceId"`
	Event         EventList          `xml:"Event"`
	MaxEnvelopes  uint32             `xml:"MaxEnvelopes"`
	CurrentTime   string             `xml:"CurrentTime"`
	RetryCount    uint32             `xml:"RetryCount"`
	ParameterList ParameterValueList `xml:"ParameterList"`
}

func (*Inform) Method() string { return "Inform" }

// HasEvent reports whether the Inform carries the event code.
func (i *Inform) HasEvent(code string) bool {
	for _, e := range i.Event {
		if e.EventCode == code {
			return true
		}
	}
	return false
}

type InformResponse struct {
	MaxEnvelopes uint32 `xml:"MaxEnvelopes"`
}

func (*InformResponse) Method() string { return "InformResponse" }

type GetRPCMethods struct{}

func (*GetRPCMethods) Method() string { return "GetRPCMethods" }

type GetRPCMethodsResponse struct {
	MethodList StringList `xml:"MethodList"`
}

func (*GetRPCMethodsResponse) Method() string { return "GetRPCMethodsResponse" }

type GetParameterNames struct {
	ParameterPath string `xml:"ParameterPath"`
	NextLevel     bool   `xml:"NextLevel"`
}

func (*GetParameterNames) Method() string { return "GetParameterNames" }

type GetParameterNamesResponse struct {
	ParameterList ParameterInfoList `xml:"ParameterList"`
}

func (*GetParameterNamesResponse) Method() string { return "GetParameterNamesResponse" }

type GetParameterValues struct {
	ParameterNames StringList `xml:"ParameterNames"`
}

func (*GetParameterValues) Method() string { return "GetParameterValues" }

type GetParameterValuesResponse struct {
	ParameterList ParameterValueList `xml:"ParameterList"`
}

func (*GetParameterValuesResponse) Method() string { return "GetParameterValuesResponse" }

type SetParameterValues struct {
	ParameterList ParameterValueList `xml:"ParameterList"`
	ParameterKey  string             `xml:"ParameterKey"`
}

func (*SetParameterValues) Method() string { return "SetParameterValues" }

type SetParameterValuesResponse struct {
	// Status is 0 when the change is applied, 1 when it is committed but
	// applied later (e.g. after a reboot).
	Status int `xml:"Status"`
}

func (*SetParameterValuesResponse) Method() string { return "SetParameterValuesResponse" }

type Reboot struct {
	CommandKey string `xml:"CommandKey"`
}

func (*Reboot) Method() string { return "Reboot" }

type RebootResponse struct{}

func (*RebootResponse) Method() string { return "RebootResponse" }

type FactoryReset struct{}

func (*FactoryReset) Method() string { return "FactoryReset" }

type FactoryResetResponse struct{}

func (*FactoryResetResponse) Method() string { return "FactoryResetResponse" }

type FaultStruct struct {
	FaultCode   uint32 `xml:"FaultCode"`
	FaultString string `xml:"FaultString"`
}

// TransferComplete is sent by the CPE after a Download or Upload. It is
// decoded so that the ACS can acknowledge it instead of faulting.
type TransferComplete struct {
	CommandKey   string      `xml:"CommandKey"`
	FaultStruct  FaultStruct `xml:"FaultStruct"`
	StartTime    string      `xml:"StartTime"`
	CompleteTime string      `xml:"CompleteTime"`
}

func (*TransferComplete) Method() string { return "TransferComplete" }

type TransferCompleteResponse struct{}

func (*TransferCompleteResponse) Method() string { return "TransferCompleteResponse" }

// Unknown is an RPC the codec has no type for. The ACS answers a CPE request
// it does not know with fault 8000.
type Unknown struct {
	Name string
}

func (u *Unknown) Method() string { return u.Name }

// CWMP fault codes of TR-069 Tables 42 (ACS) and 43 (CPE).
const (
	FaultMethodNotSupported      = 8000
	FaultRequestDenied           = 8001
	FaultInternalError           = 8002
	FaultInvalidArguments        = 8003
	FaultCPEMethodNotSupported   = 9000
	FaultCPERequestDenied        = 9001
	FaultCPEInternalError        = 9002
	FaultCPEInvalidArguments     = 9003
	FaultCPEResourcesExceeded    = 9004
	FaultCPEInvalidParamName     = 9005
	FaultCPEInvalidParamType     = 9006
	FaultCPEInvalidParamValue    = 9007
	FaultCPENonWritableParam     = 9008
	FaultCPENotificationRejected = 9009
)

type SetParameterValuesFault struct {
	ParameterName string `xml:"ParameterName"`
	FaultCode     uint32 `xml:"FaultCode"`
	FaultString   string `xml:"FaultString"`
}

// CWMPFault is the cwmp:Fault element inside a SOAP fault detail.
type CWMPFault struct {
	FaultCode               uint32                    `xml:"FaultCode"`
	FaultString             string                    `xml:"FaultString"`
	SetParameterValuesFault []SetParameterValuesFault `xml:"SetParameterValuesFault"`
}

// Fault is a SOAP fault. Detail is nil for a plain SOAP fault without a CWMP
// fault inside.
type Fault struct {
	FaultCode   string
	FaultString string
	Detail      *CWMPFault
}

func (*Fault) Method() string { return "Fault" }

// Code returns the CWMP fault code, or 0 for a plain SOAP fault.
func (f *Fault) Code() uint32 {
	if f.Detail == nil {
		return 0
	}
	return f.Detail.FaultCode
}

// String returns the most specific fault description.
func (f *Fault) String() string {
	if f.Detail != nil && f.Detail.FaultString != "" {
		return f.Detail.FaultString
	}
	return f.FaultString
}

// NewFault builds the fault the ACS sends for a CWMP fault code (8xxx).
func NewFault(code uint32, msg string) *Fault {
	return &Fault{FaultCode: "Client", FaultString: "CWMP fault", Detail: &CWMPFault{FaultCode: code, FaultString: msg}}
}

func (f *Fault) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	err := e.EncodeToken(start)
	if err == nil {
		err = e.EncodeElement(f.FaultCode, xml.StartElement{Name: xml.Name{Local: "faultcode"}})
	}
	if err == nil {
		err = e.EncodeElement(f.FaultString, xml.StartElement{Name: xml.Name{Local: "faultstring"}})
	}
	if err == nil && f.Detail != nil {
		detail := xml.StartElement{Name: xml.Name{Local: "detail"}}
		err = e.EncodeToken(detail)
		if err == nil {
			err = e.EncodeElement(f.Detail, xml.StartElement{Name: xml.Name{Local: "cwmp:Fault"}})
		}
		if err == nil {
			err = e.EncodeToken(detail.End())
		}
	}
	if err == nil {
		err = e.EncodeToken(start.End())
	}
	return err
}

func (f *Fault) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var aux struct {
		FaultCode   string     `xml:"faultcode"`
		FaultString string     `xml:"faultstring"`
		Detail      *CWMPFault `xml:"detail>Fault"`
	}
	if err := d.DecodeElement(&aux, &start); err != nil {
		return err
	}
	*f = Fault{FaultCode: aux.FaultCode, FaultString: aux.FaultString, Detail: aux.Detail}
	return nil
}

// newMessage returns an empty message for a body element name, or nil.
func newMessage(method string) Message {
	switch method {
	case "Inform":
		return &Inform{}
	case "InformResponse":
		return &InformResponse{}
	case "GetRPCMethods":
		return &GetRPCMethods{}
	case "GetRPCMethodsResponse":
		return &GetRPCMethodsResponse{}
	case "GetParameterNames":
		return &GetParameterNames{}
	case "GetParameterNamesResponse":
		return &GetParameterNamesResponse{}
	case "GetParameterValues":
		return &GetParameterValues{}
	case "GetParameterValuesResponse":
		return &GetParameterValuesResponse{}
	case "SetParameterValues":
		return &SetParameterValues{}
	case "SetParameterValuesResponse":
		return &SetParameterValuesResponse{}
	case "Reboot":
		return &Reboot{}
	case "RebootResponse":
		return &RebootResponse{}
	case "FactoryReset":
		return &FactoryReset{}
	case "FactoryResetResponse":
		return &FactoryResetResponse{}
	case "TransferComplete":
		return &TransferComplete{}
	case "TransferCompleteResponse":
		return &TransferCompleteResponse{}
	case "Fault":
		return &Fault{}
	}
	return nil
}
