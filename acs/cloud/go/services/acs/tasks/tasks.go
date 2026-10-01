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

// Package tasks defines the operations an operator can queue for a CPE and
// the CWMP requests that carry them out.
package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/datamodel"
)

// Task types.
const (
	TypeReboot             = "reboot"
	TypeFactoryReset       = "factory_reset"
	TypeRefresh            = "refresh"
	TypeGetParameterValues = "get_parameter_values"
	TypeSetParameterValues = "set_parameter_values"
	TypeGetParameterNames  = "get_parameter_names"
)

// Types lists every task type.
var Types = []string{
	TypeReboot, TypeFactoryReset, TypeRefresh, TypeGetParameterValues, TypeSetParameterValues, TypeGetParameterNames,
}

type ParameterValue struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Type is the xsi:type, e.g. "xsd:unsignedInt"; "" means xsd:string.
	Type string `json:"type,omitempty"`
}

// Args are the arguments of a task; which fields apply depends on its type.
type Args struct {
	ParameterNames  []string         `json:"parameter_names,omitempty"`
	ParameterValues []ParameterValue `json:"parameter_values,omitempty"`
	ParameterPath   string           `json:"parameter_path,omitempty"`
	NextLevel       bool             `json:"next_level,omitempty"`
}

type ParameterInfo struct {
	Name     string `json:"name"`
	Writable bool   `json:"writable"`
}

// StepFault is a fault the task tolerated, e.g. a refresh path the CPE does
// not have.
type StepFault struct {
	Request     string `json:"request"`
	FaultCode   int    `json:"fault_code"`
	FaultString string `json:"fault_string"`
}

// Result is what a task collected from the CPE.
type Result struct {
	Values map[string]string `json:"values,omitempty"`
	Names  []ParameterInfo   `json:"names,omitempty"`
	// Status is the SetParameterValues status: 0 applied, 1 applied later.
	Status *int        `json:"status,omitempty"`
	Faults []StepFault `json:"faults,omitempty"`
}

// Validate checks the arguments of a task of the type.
func Validate(typ string, a Args) error {
	switch typ {
	case TypeReboot, TypeFactoryReset, TypeRefresh:
		if len(a.ParameterNames) > 0 || len(a.ParameterValues) > 0 || a.ParameterPath != "" {
			return fmt.Errorf("%s takes no parameters", typ)
		}
	case TypeGetParameterValues:
		if len(a.ParameterNames) == 0 {
			return errors.New("get_parameter_values needs parameter_names")
		}
		for _, n := range a.ParameterNames {
			if strings.TrimSpace(n) == "" {
				return errors.New("parameter_names cannot contain an empty name")
			}
		}
	case TypeSetParameterValues:
		if len(a.ParameterValues) == 0 {
			return errors.New("set_parameter_values needs parameter_values")
		}
		for _, p := range a.ParameterValues {
			if p.Name == "" || strings.HasSuffix(p.Name, ".") {
				return fmt.Errorf("%q is not a parameter name", p.Name)
			}
			if p.Type != "" && !strings.HasPrefix(p.Type, "xsd:") {
				return fmt.Errorf("type %q of %s is not an xsd type", p.Type, p.Name)
			}
		}
	case TypeGetParameterNames:
		if a.ParameterPath != "" && !strings.HasSuffix(a.ParameterPath, ".") && a.NextLevel {
			return errors.New("next_level needs a partial path ending in '.'")
		}
	default:
		return fmt.Errorf("unknown task type %q; want one of %s", typ, strings.Join(Types, ", "))
	}
	return nil
}

// ParseArgs decodes stored arguments.
func ParseArgs(s string) (Args, error) {
	a := Args{}
	if s == "" {
		return a, nil
	}
	err := json.Unmarshal([]byte(s), &a)
	return a, err
}

// ParseResult decodes a stored result.
func ParseResult(s string) (Result, error) {
	r := Result{}
	if s == "" {
		return r, nil
	}
	err := json.Unmarshal([]byte(s), &r)
	return r, err
}

// Plan returns the CWMP requests that execute a task, in order. commandKey is
// echoed by the CPE in the M Reboot event and as the ParameterKey.
func Plan(typ string, a Args, h datamodel.Handler, root, commandKey string) ([]cwmp.Message, error) {
	switch typ {
	case TypeReboot:
		return []cwmp.Message{&cwmp.Reboot{CommandKey: commandKey}}, nil
	case TypeFactoryReset:
		return []cwmp.Message{&cwmp.FactoryReset{}}, nil
	case TypeGetParameterValues:
		return gpvRequests(a.ParameterNames, h.Quirks().MaxGPVNames), nil
	case TypeSetParameterValues:
		list := make(cwmp.ParameterValueList, 0, len(a.ParameterValues))
		for _, p := range a.ParameterValues {
			list = append(list, cwmp.ParameterValueStruct{Name: p.Name, Value: p.Value, Type: p.Type})
		}
		return []cwmp.Message{&cwmp.SetParameterValues{ParameterList: list, ParameterKey: commandKey}}, nil
	case TypeGetParameterNames:
		path := a.ParameterPath
		if path == "" {
			path = root
		}
		return []cwmp.Message{&cwmp.GetParameterNames{ParameterPath: path, NextLevel: a.NextLevel}}, nil
	case TypeRefresh:
		var out []cwmp.Message
		for _, p := range h.RefreshPaths(root) {
			out = append(out, &cwmp.GetParameterValues{ParameterNames: cwmp.StringList{p}})
		}
		return out, nil
	}
	return nil, fmt.Errorf("unknown task type %q", typ)
}

func gpvRequests(names []string, limit int) []cwmp.Message {
	var out []cwmp.Message
	for len(names) > 0 {
		n := len(names)
		if limit > 0 && n > limit {
			n = limit
		}
		out = append(out, &cwmp.GetParameterValues{ParameterNames: append(cwmp.StringList{}, names[:n]...)})
		names = names[n:]
	}
	return out
}

// Apply merges the CPE's answer to one request of the plan into the result.
// It returns an error for a response of the wrong type.
func Apply(r *Result, resp cwmp.Message) error {
	switch m := resp.(type) {
	case *cwmp.GetParameterValuesResponse:
		if r.Values == nil {
			r.Values = map[string]string{}
		}
		for _, p := range m.ParameterList {
			r.Values[p.Name] = p.Value
		}
	case *cwmp.GetParameterNamesResponse:
		for _, p := range m.ParameterList {
			r.Names = append(r.Names, ParameterInfo{Name: p.Name, Writable: p.Writable})
		}
	case *cwmp.SetParameterValuesResponse:
		status := m.Status
		r.Status = &status
	case *cwmp.RebootResponse, *cwmp.FactoryResetResponse:
	default:
		return fmt.Errorf("unexpected %s", resp.Method())
	}
	return nil
}

// Tolerated reports whether a fault on one request of the plan leaves the
// task running, e.g. a refresh path the CPE does not have.
func Tolerated(typ string, f *cwmp.Fault) bool {
	return typ == TypeRefresh && f.Code() == cwmp.FaultCPEInvalidParamName
}

// Retryable reports whether a fault is worth trying again in a later session.
func Retryable(f *cwmp.Fault) bool {
	switch f.Code() {
	case cwmp.FaultCPEInternalError, cwmp.FaultCPEResourcesExceeded:
		return true
	}
	return false
}
