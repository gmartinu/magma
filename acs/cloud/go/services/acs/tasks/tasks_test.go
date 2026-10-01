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

package tasks_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/tasks"
)

func TestValidate(t *testing.T) {
	ok := []struct {
		typ  string
		args tasks.Args
	}{
		{tasks.TypeReboot, tasks.Args{}},
		{tasks.TypeFactoryReset, tasks.Args{}},
		{tasks.TypeRefresh, tasks.Args{}},
		{tasks.TypeGetParameterValues, tasks.Args{ParameterNames: []string{"Device.DeviceInfo."}}},
		{tasks.TypeSetParameterValues, tasks.Args{ParameterValues: []tasks.ParameterValue{{Name: "Device.A", Value: "1", Type: "xsd:int"}}}},
		{tasks.TypeGetParameterNames, tasks.Args{ParameterPath: "Device.", NextLevel: true}},
		{tasks.TypeGetParameterNames, tasks.Args{}},
	}
	for _, tc := range ok {
		assert.NoError(t, tasks.Validate(tc.typ, tc.args), tc.typ)
	}
	bad := []struct {
		typ  string
		args tasks.Args
	}{
		{"upgrade", tasks.Args{}},
		{tasks.TypeReboot, tasks.Args{ParameterNames: []string{"x"}}},
		{tasks.TypeGetParameterValues, tasks.Args{}},
		{tasks.TypeGetParameterValues, tasks.Args{ParameterNames: []string{" "}}},
		{tasks.TypeSetParameterValues, tasks.Args{}},
		{tasks.TypeSetParameterValues, tasks.Args{ParameterValues: []tasks.ParameterValue{{Name: "Device.A.", Value: "1"}}}},
		{tasks.TypeSetParameterValues, tasks.Args{ParameterValues: []tasks.ParameterValue{{Name: "Device.A", Type: "int"}}}},
		{tasks.TypeGetParameterNames, tasks.Args{ParameterPath: "Device.A", NextLevel: true}},
	}
	for _, tc := range bad {
		assert.Error(t, tasks.Validate(tc.typ, tc.args), "%s %+v", tc.typ, tc.args)
	}
}

func TestPlan(t *testing.T) {
	g := datamodel.Generic()
	plan, err := tasks.Plan(tasks.TypeReboot, tasks.Args{}, g, datamodel.RootTR181, "k1")
	require.NoError(t, err)
	assert.Equal(t, []cwmp.Message{&cwmp.Reboot{CommandKey: "k1"}}, plan)

	plan, _ = tasks.Plan(tasks.TypeFactoryReset, tasks.Args{}, g, datamodel.RootTR181, "k1")
	assert.Equal(t, []cwmp.Message{&cwmp.FactoryReset{}}, plan)

	plan, _ = tasks.Plan(tasks.TypeSetParameterValues, tasks.Args{ParameterValues: []tasks.ParameterValue{{Name: "Device.A", Value: "1"}}}, g, datamodel.RootTR181, "k2")
	assert.Equal(t, []cwmp.Message{&cwmp.SetParameterValues{ParameterList: cwmp.ParameterValueList{{Name: "Device.A", Value: "1"}}, ParameterKey: "k2"}}, plan)

	plan, _ = tasks.Plan(tasks.TypeGetParameterNames, tasks.Args{}, g, datamodel.RootTR098, "")
	assert.Equal(t, []cwmp.Message{&cwmp.GetParameterNames{ParameterPath: datamodel.RootTR098}}, plan)

	plan, _ = tasks.Plan(tasks.TypeRefresh, tasks.Args{}, g, datamodel.RootTR181, "")
	require.Len(t, plan, len(g.RefreshPaths(datamodel.RootTR181)))
	assert.Equal(t, &cwmp.GetParameterValues{ParameterNames: cwmp.StringList{"Device.DeviceInfo."}}, plan[0])

	names := []string{"a", "b", "c", "d", "e"}
	plan, _ = tasks.Plan(tasks.TypeGetParameterValues, tasks.Args{ParameterNames: names}, g, datamodel.RootTR181, "")
	assert.Equal(t, []cwmp.Message{&cwmp.GetParameterValues{ParameterNames: cwmp.StringList(names)}}, plan)
	capped := &datamodel.Spec{HandlerName: "capped", Quirk: datamodel.Quirks{MaxGPVNames: 2}}
	plan, _ = tasks.Plan(tasks.TypeGetParameterValues, tasks.Args{ParameterNames: names}, capped, datamodel.RootTR181, "")
	assert.Equal(t, []cwmp.Message{
		&cwmp.GetParameterValues{ParameterNames: cwmp.StringList{"a", "b"}},
		&cwmp.GetParameterValues{ParameterNames: cwmp.StringList{"c", "d"}},
		&cwmp.GetParameterValues{ParameterNames: cwmp.StringList{"e"}},
	}, plan)

	_, err = tasks.Plan("nope", tasks.Args{}, g, "", "")
	assert.Error(t, err)
}

func TestApplyAndFaults(t *testing.T) {
	r := tasks.Result{}
	require.NoError(t, tasks.Apply(&r, &cwmp.GetParameterValuesResponse{ParameterList: cwmp.ParameterValueList{{Name: "A", Value: "1"}}}))
	require.NoError(t, tasks.Apply(&r, &cwmp.GetParameterValuesResponse{ParameterList: cwmp.ParameterValueList{{Name: "B", Value: "2"}}}))
	require.NoError(t, tasks.Apply(&r, &cwmp.GetParameterNamesResponse{ParameterList: cwmp.ParameterInfoList{{Name: "A", Writable: true}}}))
	require.NoError(t, tasks.Apply(&r, &cwmp.SetParameterValuesResponse{Status: 1}))
	require.NoError(t, tasks.Apply(&r, &cwmp.RebootResponse{}))
	assert.Error(t, tasks.Apply(&r, &cwmp.InformResponse{}))
	one := 1
	assert.Equal(t, tasks.Result{
		Values: map[string]string{"A": "1", "B": "2"},
		Names:  []tasks.ParameterInfo{{Name: "A", Writable: true}},
		Status: &one,
	}, r)

	invalidName := cwmp.NewFault(cwmp.FaultCPEInvalidParamName, "no such parameter")
	assert.True(t, tasks.Tolerated(tasks.TypeRefresh, invalidName))
	assert.False(t, tasks.Tolerated(tasks.TypeGetParameterValues, invalidName))
	assert.False(t, tasks.Retryable(invalidName))
	assert.True(t, tasks.Retryable(cwmp.NewFault(cwmp.FaultCPEInternalError, "")))
	assert.True(t, tasks.Retryable(cwmp.NewFault(cwmp.FaultCPEResourcesExceeded, "")))
	assert.False(t, tasks.Retryable(&cwmp.Fault{FaultCode: "Server"}))

	a, err := tasks.ParseArgs(`{"parameter_names":["A"]}`)
	require.NoError(t, err)
	assert.Equal(t, []string{"A"}, a.ParameterNames)
	a, err = tasks.ParseArgs("")
	assert.NoError(t, err)
	assert.Equal(t, tasks.Args{}, a)
	res, err := tasks.ParseResult(`{"values":{"A":"1"}}`)
	require.NoError(t, err)
	assert.Equal(t, "1", res.Values["A"])
	_, err = tasks.ParseResult("{")
	assert.Error(t, err)
}
