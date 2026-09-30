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

package cwmp_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
)

// Inform in the shape of the TR-069 Amendment 6 examples, cwmp-1-0.
const informV10 = `<?xml version="1.0" encoding="UTF-8"?>
<soap-env:Envelope xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:cwmp="urn:dslforum-org:cwmp-1-0">
  <soap-env:Header>
    <cwmp:ID soap-env:mustUnderstand="1">1234</cwmp:ID>
    <cwmp:NoMoreRequests>1</cwmp:NoMoreRequests>
  </soap-env:Header>
  <soap-env:Body>
    <cwmp:Inform>
      <DeviceId>
        <Manufacturer>Global Telecom</Manufacturer>
        <OUI>00A1B2</OUI>
        <ProductClass>Titan4000</ProductClass>
        <SerialNumber>GT0001</SerialNumber>
      </DeviceId>
      <Event soap-enc:arrayType="cwmp:EventStruct[2]">
        <EventStruct><EventCode>0 BOOTSTRAP</EventCode><CommandKey></CommandKey></EventStruct>
        <EventStruct><EventCode>1 BOOT</EventCode><CommandKey/></EventStruct>
      </Event>
      <MaxEnvelopes>1</MaxEnvelopes>
      <CurrentTime>2026-09-30T12:00:00Z</CurrentTime>
      <RetryCount>0</RetryCount>
      <ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[2]">
        <ParameterValueStruct>
          <Name>Device.DeviceInfo.SoftwareVersion</Name>
          <Value xsi:type="xsd:string">1.2.3</Value>
        </ParameterValueStruct>
        <ParameterValueStruct>
          <Name>Device.ManagementServer.ConnectionRequestURL</Name>
          <Value xsi:type="xsd:string">http://10.0.0.1:7547/cr?a=1&amp;b=2</Value>
        </ParameterValueStruct>
      </ParameterList>
    </cwmp:Inform>
  </soap-env:Body>
</soap-env:Envelope>`

// Same Inform from a cwmp-1-2 stack using other prefixes and a default
// namespace for the RPC element.
const informV12 = `<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://schemas.xmlsoap.org/soap/envelope/" xmlns:SOAP-ENC="http://schemas.xmlsoap.org/soap/encoding/">
<SOAP-ENV:Header><ID xmlns="urn:dslforum-org:cwmp-1-2" SOAP-ENV:mustUnderstand="1">abc</ID></SOAP-ENV:Header>
<SOAP-ENV:Body><Inform xmlns="urn:dslforum-org:cwmp-1-2"><DeviceId><Manufacturer>X</Manufacturer><OUI>001122</OUI><ProductClass></ProductClass><SerialNumber>S1</SerialNumber></DeviceId>
<Event><EventStruct><EventCode>2 PERIODIC</EventCode><CommandKey></CommandKey></EventStruct></Event>
<MaxEnvelopes>1</MaxEnvelopes><CurrentTime>0001-01-01T00:00:00Z</CurrentTime><RetryCount> 3 </RetryCount>
<ParameterList><ParameterValueStruct><Name>InternetGatewayDevice.DeviceInfo.HardwareVersion</Name><Value>HW1</Value></ParameterValueStruct></ParameterList>
</Inform></SOAP-ENV:Body></SOAP-ENV:Envelope>`

func TestDecodeInform(t *testing.T) {
	env, err := cwmp.Decode([]byte(informV10))
	require.NoError(t, err)
	assert.Equal(t, "1234", env.ID)
	assert.Equal(t, cwmp.NSCwmp10, env.Namespace)
	assert.True(t, env.NoMoreRequests)

	inform, ok := env.Body.(*cwmp.Inform)
	require.True(t, ok)
	assert.Equal(t, "00A1B2-Titan4000-GT0001", inform.DeviceId.DeviceID())
	assert.Equal(t, cwmp.EventList{{EventCode: "0 BOOTSTRAP"}, {EventCode: "1 BOOT"}}, inform.Event)
	assert.True(t, inform.HasEvent(cwmp.EventBootstrap))
	assert.False(t, inform.HasEvent(cwmp.EventPeriodic))
	assert.Equal(t, uint32(1), inform.MaxEnvelopes)
	assert.Equal(t, map[string]string{
		"Device.DeviceInfo.SoftwareVersion":            "1.2.3",
		"Device.ManagementServer.ConnectionRequestURL": "http://10.0.0.1:7547/cr?a=1&b=2",
	}, inform.ParameterList.Map())
	assert.Equal(t, "xsd:string", inform.ParameterList[0].Type)

	env, err = cwmp.Decode([]byte(informV12))
	require.NoError(t, err)
	assert.Equal(t, "abc", env.ID)
	assert.Equal(t, cwmp.NSCwmp12, env.Namespace)
	inform = env.Body.(*cwmp.Inform)
	assert.Equal(t, "001122-S1", inform.DeviceId.DeviceID())
	assert.Equal(t, uint32(3), inform.RetryCount)
	assert.Equal(t, "", inform.ParameterList[0].Type)
	assert.Equal(t, "HW1", inform.ParameterList[0].Value)
}

func TestEncodeAnswersInCPENamespace(t *testing.T) {
	for _, ns := range []string{cwmp.NSCwmp10, cwmp.NSCwmp11, cwmp.NSCwmp12, cwmp.NSCwmp13, cwmp.NSCwmp14} {
		out, err := cwmp.Encode(&cwmp.Envelope{ID: "1", Namespace: ns, Body: &cwmp.InformResponse{MaxEnvelopes: 1}})
		require.NoError(t, err)
		s := string(out)
		assert.Contains(t, s, `xmlns:cwmp="`+ns+`"`)
		assert.Contains(t, s, `<cwmp:ID soap-env:mustUnderstand="1">1</cwmp:ID>`)
		assert.Contains(t, s, `<cwmp:InformResponse><MaxEnvelopes>1</MaxEnvelopes></cwmp:InformResponse>`)
		assert.NotContains(t, s, "HoldRequests")

		env, err := cwmp.Decode(out)
		require.NoError(t, err)
		assert.Equal(t, ns, env.Namespace)
	}

	out, err := cwmp.Encode(&cwmp.Envelope{ID: "x", Body: &cwmp.GetRPCMethods{}})
	require.NoError(t, err)
	assert.Contains(t, string(out), `xmlns:cwmp="`+cwmp.NSCwmp10+`"`)
	assert.Contains(t, string(out), `<cwmp:GetRPCMethods></cwmp:GetRPCMethods>`)
}

func TestEncodeWireFormat(t *testing.T) {
	out, err := cwmp.Encode(&cwmp.Envelope{ID: "7", HoldRequests: true, Body: &cwmp.SetParameterValues{
		ParameterList: cwmp.ParameterValueList{
			{Name: "Device.ManagementServer.PeriodicInformInterval", Value: "300", Type: "xsd:unsignedInt"},
			{Name: "Device.X.Name", Value: "a<b&c"},
		},
		ParameterKey: "k1",
	}})
	require.NoError(t, err)
	s := string(out)
	assert.Contains(t, s, `<cwmp:HoldRequests soap-env:mustUnderstand="1">1</cwmp:HoldRequests>`)
	assert.Contains(t, s, `<ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[2]">`)
	assert.Contains(t, s, `<Value xsi:type="xsd:unsignedInt">300</Value>`)
	assert.Contains(t, s, `<Value xsi:type="xsd:string">a&lt;b&amp;c</Value>`)
	assert.Contains(t, s, `<ParameterKey>k1</ParameterKey>`)

	out, err = cwmp.Encode(&cwmp.Envelope{ID: "8", Body: &cwmp.GetParameterValues{
		ParameterNames: cwmp.StringList{"Device.DeviceInfo.", "Device.Cellular."},
	}})
	require.NoError(t, err)
	assert.Contains(t, string(out), `<ParameterNames soap-enc:arrayType="xsd:string[2]"><string>Device.DeviceInfo.</string><string>Device.Cellular.</string></ParameterNames>`)

	env, err := cwmp.Decode(out)
	require.NoError(t, err)
	assert.True(t, !env.HoldRequests)
}

func TestRoundTrip(t *testing.T) {
	msgs := []cwmp.Message{
		&cwmp.Inform{
			DeviceId:      cwmp.DeviceIDStruct{Manufacturer: "M", OUI: "O", ProductClass: "P", SerialNumber: "S"},
			Event:         cwmp.EventList{{EventCode: cwmp.EventMReboot, CommandKey: "t1"}},
			MaxEnvelopes:  1,
			CurrentTime:   "2026-09-30T12:00:00Z",
			ParameterList: cwmp.ParameterValueList{{Name: "Device.A", Value: "1", Type: "xsd:int"}},
		},
		&cwmp.InformResponse{MaxEnvelopes: 1},
		&cwmp.GetRPCMethods{},
		&cwmp.GetRPCMethodsResponse{MethodList: cwmp.StringList{"Inform", "GetRPCMethods"}},
		&cwmp.GetParameterNames{ParameterPath: "Device.", NextLevel: true},
		&cwmp.GetParameterNamesResponse{ParameterList: cwmp.ParameterInfoList{{Name: "Device.A", Writable: true}, {Name: "Device.B."}}},
		&cwmp.GetParameterValues{ParameterNames: cwmp.StringList{"Device.A"}},
		&cwmp.GetParameterValuesResponse{ParameterList: cwmp.ParameterValueList{{Name: "Device.A", Value: "x", Type: "xsd:string"}}},
		&cwmp.SetParameterValues{ParameterList: cwmp.ParameterValueList{{Name: "Device.A", Value: "y", Type: "xsd:string"}}, ParameterKey: "k"},
		&cwmp.SetParameterValuesResponse{Status: 1},
		&cwmp.Reboot{CommandKey: "task-1"},
		&cwmp.RebootResponse{},
		&cwmp.FactoryReset{},
		&cwmp.FactoryResetResponse{},
		&cwmp.TransferComplete{CommandKey: "d1", FaultStruct: cwmp.FaultStruct{FaultCode: 9010, FaultString: "download failed"}},
		&cwmp.TransferCompleteResponse{},
		cwmp.NewFault(cwmp.FaultMethodNotSupported, "Method not supported"),
		&cwmp.Fault{FaultCode: "Client", FaultString: "CWMP fault", Detail: &cwmp.CWMPFault{
			FaultCode:   cwmp.FaultCPEInvalidArguments,
			FaultString: "Invalid arguments",
			SetParameterValuesFault: []cwmp.SetParameterValuesFault{
				{ParameterName: "Device.A", FaultCode: cwmp.FaultCPEInvalidParamValue, FaultString: "bad value"},
			},
		}},
		&cwmp.Fault{FaultCode: "Server", FaultString: "no detail"},
	}
	for _, msg := range msgs {
		t.Run(msg.Method(), func(t *testing.T) {
			out, err := cwmp.Encode(&cwmp.Envelope{ID: "id-1", Namespace: cwmp.NSCwmp12, Body: msg})
			require.NoError(t, err)
			env, err := cwmp.Decode(out)
			require.NoError(t, err, string(out))
			assert.Equal(t, "id-1", env.ID)
			assert.Equal(t, cwmp.NSCwmp12, env.Namespace)
			assert.Equal(t, msg, env.Body)
		})
	}
}

// CPE fault shaped like the SetParameterValues fault example of TR-069.
const spvFault = `<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/" xmlns:cwmp="urn:dslforum-org:cwmp-1-1">
<soapenv:Header><cwmp:ID soapenv:mustUnderstand="1">42</cwmp:ID></soapenv:Header>
<soapenv:Body><soapenv:Fault><faultcode>Client</faultcode><faultstring>CWMP fault</faultstring>
<detail><cwmp:Fault><FaultCode>9003</FaultCode><FaultString>Invalid arguments</FaultString>
<SetParameterValuesFault><ParameterName>Device.Time.LocalTimeZoneName</ParameterName><FaultCode>9008</FaultCode><FaultString>Attempt to set a non-writable parameter</FaultString></SetParameterValuesFault>
<SetParameterValuesFault><ParameterName>Device.Time.NTPServer1</ParameterName><FaultCode>9007</FaultCode><FaultString>Invalid parameter value</FaultString></SetParameterValuesFault>
</cwmp:Fault></detail></soapenv:Fault></soapenv:Body></soapenv:Envelope>`

func TestDecodeFaults(t *testing.T) {
	env, err := cwmp.Decode([]byte(spvFault))
	require.NoError(t, err)
	assert.Equal(t, "42", env.ID)
	assert.Equal(t, cwmp.NSCwmp11, env.Namespace)
	fault, ok := env.Body.(*cwmp.Fault)
	require.True(t, ok)
	assert.Equal(t, uint32(9003), fault.Code())
	assert.Equal(t, "Invalid arguments", fault.String())
	require.Len(t, fault.Detail.SetParameterValuesFault, 2)
	assert.Equal(t, uint32(9007), fault.Detail.SetParameterValuesFault[1].FaultCode)

	plain := `<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/"><s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>Bad envelope</faultstring></s:Fault></s:Body></s:Envelope>`
	env, err = cwmp.Decode([]byte(plain))
	require.NoError(t, err)
	fault = env.Body.(*cwmp.Fault)
	assert.Equal(t, uint32(0), fault.Code())
	assert.Equal(t, "Bad envelope", fault.String())
	assert.Equal(t, "", env.Namespace)
}

func TestDecodeEdgeCases(t *testing.T) {
	_, err := cwmp.Decode(nil)
	assert.ErrorIs(t, err, cwmp.ErrEmpty)
	_, err = cwmp.Decode([]byte(" \r\n "))
	assert.ErrorIs(t, err, cwmp.ErrEmpty)
	assert.True(t, cwmp.IsEmpty([]byte("\n")))

	_, err = cwmp.Decode([]byte("405 Method Not Allowed"))
	assert.Error(t, err)
	_, err = cwmp.Decode([]byte(`<html><body>hi</body></html>`))
	assert.Error(t, err)
	_, err = cwmp.Decode([]byte(`<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/"><soap-env:Body></soap-env:Body></soap-env:Envelope>`))
	assert.Error(t, err)

	unknown := strings.Replace(informV10, "cwmp:Inform>", "cwmp:Kicked>", 2)
	env, err := cwmp.Decode([]byte(unknown))
	require.NoError(t, err)
	assert.Equal(t, &cwmp.Unknown{Name: "Kicked"}, env.Body)
	_, err = cwmp.Encode(&cwmp.Envelope{Body: env.Body})
	assert.Error(t, err)

	hold := `<e:Envelope xmlns:e="http://schemas.xmlsoap.org/soap/envelope/" xmlns:c="urn:dslforum-org:cwmp-1-4"><e:Header><c:ID>9</c:ID><c:HoldRequests>true</c:HoldRequests></e:Header><e:Body><c:RebootResponse/></e:Body></e:Envelope>`
	env, err = cwmp.Decode([]byte(hold))
	require.NoError(t, err)
	assert.True(t, env.HoldRequests)
	assert.Equal(t, cwmp.NSCwmp14, env.Namespace)
	assert.Equal(t, &cwmp.RebootResponse{}, env.Body)

	latin1 := append([]byte(`<?xml version="1.0" encoding="ISO-8859-1"?><e:Envelope xmlns:e="http://schemas.xmlsoap.org/soap/envelope/" xmlns:c="urn:dslforum-org:cwmp-1-0"><e:Body><c:GetParameterValuesResponse><ParameterList><ParameterValueStruct><Name>Device.X</Name><Value>S`),
		0xe3, 'o')
	latin1 = append(latin1, []byte(`</Value></ParameterValueStruct></ParameterList></c:GetParameterValuesResponse></e:Body></e:Envelope>`)...)
	env, err = cwmp.Decode(latin1)
	require.NoError(t, err)
	assert.Equal(t, "São", env.Body.(*cwmp.GetParameterValuesResponse).ParameterList[0].Value)
}
