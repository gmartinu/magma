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

package simulator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/datamodel"
)

func TestHandle(t *testing.T) {
	c := New("http://acs", datamodel.RootTR181, cwmp.DeviceIDStruct{OUI: "O", ProductClass: "P", SerialNumber: "S"}, "u", "p")

	gpn := c.handle(&cwmp.GetParameterNames{ParameterPath: "Device.Cellular.", NextLevel: true}).(*cwmp.GetParameterNamesResponse)
	assert.Equal(t, cwmp.ParameterInfoList{{Name: "Device.Cellular.AccessPoint."}, {Name: "Device.Cellular.Interface."}}, gpn.ParameterList)

	gpv := c.handle(&cwmp.GetParameterValues{ParameterNames: cwmp.StringList{"Device.Cellular.Interface.1.RSRP"}}).(*cwmp.GetParameterValuesResponse)
	assert.Equal(t, cwmp.ParameterValueList{{Name: "Device.Cellular.Interface.1.RSRP", Value: "-95", Type: "xsd:int"}}, gpv.ParameterList)
	f := c.handle(&cwmp.GetParameterValues{ParameterNames: cwmp.StringList{"Device.Nope."}}).(*cwmp.Fault)
	assert.Equal(t, uint32(cwmp.FaultCPEInvalidParamName), f.Code())

	f = c.handle(&cwmp.SetParameterValues{ParameterList: cwmp.ParameterValueList{
		{Name: "Device.DeviceInfo.SerialNumber", Value: "x"}, {Name: "Device.Nope", Value: "x"},
	}}).(*cwmp.Fault)
	assert.Equal(t, uint32(cwmp.FaultCPEInvalidArguments), f.Code())
	require.Len(t, f.Detail.SetParameterValuesFault, 2)
	assert.Equal(t, uint32(cwmp.FaultCPENonWritableParam), f.Detail.SetParameterValuesFault[0].FaultCode)

	c.handle(&cwmp.SetParameterValues{ParameterList: cwmp.ParameterValueList{{Name: "Device.ManagementServer.Password", Value: "new"}}, ParameterKey: "k"})
	assert.Equal(t, "new", c.Password)
	assert.Equal(t, "k", c.Params["Device.ManagementServer.ParameterKey"].Value)

	c.handle(&cwmp.Reboot{CommandKey: "r1"})
	assert.Equal(t, []cwmp.EventStruct{{EventCode: cwmp.EventBoot}, {EventCode: cwmp.EventMReboot, CommandKey: "r1"}}, c.pendingEvents)
	c.handle(&cwmp.FactoryReset{})
	assert.Equal(t, "p", c.Password)
	assert.Equal(t, cwmp.EventBootstrap, c.pendingEvents[0].EventCode)

	c.Faults["Reboot"] = cwmp.NewFault(9002, "busy")
	assert.Equal(t, c.Faults["Reboot"], c.handle(&cwmp.Reboot{}))
	assert.Equal(t, uint32(cwmp.FaultCPEMethodNotSupported), c.handle(&cwmp.GetRPCMethods{}).(*cwmp.Fault).Code())
}
