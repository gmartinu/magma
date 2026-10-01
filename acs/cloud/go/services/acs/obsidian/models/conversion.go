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

package models

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-openapi/strfmt"

	"magma/acs/cloud/go/services/acs/sessionlog"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/acs/cloud/go/services/acs/tasks"
)

// FromStorage fills the device model; online is derived by the caller's
// online policy.
func (m *AcsDevice) FromStorage(d *storage.Device, online bool) *AcsDevice {
	m.DeviceID = d.DeviceID
	m.Oui = d.OUI
	m.ProductClass = d.ProductClass
	m.SerialNumber = d.SerialNumber
	m.Model = d.Model
	m.Firmware = d.Firmware
	m.Handler = d.Handler
	m.FirstSeen = strfmt.DateTime(time.Unix(d.FirstSeenSec, 0).UTC())
	m.LastSeen = strfmt.DateTime(time.Unix(d.LastSeenSec, 0).UTC())
	m.Online = online
	m.PeriodicInformInterval = d.InformIntervalSec
	return m
}

func (m *AcsTask) FromStorage(t *storage.Task) (*AcsTask, error) {
	args, err := tasks.ParseArgs(t.Args)
	if err != nil {
		return nil, fmt.Errorf("task %s arguments: %w", t.TaskID, err)
	}
	m.ID = t.TaskID
	m.DeviceID = t.DeviceID
	m.Type = t.Type
	m.Status = t.Status
	m.Attempts = int64(t.Attempts)
	m.MaxAttempts = int64(t.MaxAttempts)
	m.ParameterNames = args.ParameterNames
	m.ParameterPath = args.ParameterPath
	m.NextLevel = args.NextLevel
	for _, p := range args.ParameterValues {
		m.ParameterValues = append(m.ParameterValues, AcsParameterValue{Name: p.Name, Value: p.Value, Type: p.Type})
	}
	if t.Result != "" {
		var result interface{}
		if err := json.Unmarshal([]byte(t.Result), &result); err != nil {
			return nil, fmt.Errorf("task %s result: %w", t.TaskID, err)
		}
		m.Result = result
	}
	m.FaultCode = int64(t.FaultCode)
	m.FaultString = t.FaultString
	m.Created = strfmt.DateTime(time.Unix(t.CreatedSec, 0).UTC())
	m.Updated = strfmt.DateTime(time.Unix(t.UpdatedSec, 0).UTC())
	if t.DeadlineSec > 0 {
		m.Deadline = strfmt.DateTime(time.Unix(t.DeadlineSec, 0).UTC())
	}
	return m, nil
}

// ToArgs returns the task arguments of the request.
func (m *AcsTaskRequest) ToArgs() tasks.Args {
	a := tasks.Args{ParameterNames: m.ParameterNames, ParameterPath: m.ParameterPath, NextLevel: m.NextLevel}
	for _, p := range m.ParameterValues {
		a.ParameterValues = append(a.ParameterValues, tasks.ParameterValue{Name: p.Name, Value: p.Value, Type: p.Type})
	}
	return a
}

func (m *AcsSessionLog) FromRecord(r *sessionlog.Record) *AcsSessionLog {
	t := strfmt.DateTime(time.UnixMilli(r.TimestampMs).UTC())
	event := r.Event
	m.Time = &t
	m.Event = &event
	m.DeviceID = r.DeviceID
	m.NetworkID = r.NetworkID
	m.SessionID = r.SessionID
	m.SourceIP = r.SourceIP
	m.EventCodes = r.EventCodes
	m.Handler = r.Handler
	m.Bootstrap = r.Bootstrap
	m.RPC = r.RPC
	m.TaskID = r.TaskID
	m.TaskType = r.TaskType
	m.FaultCode = int64(r.FaultCode)
	m.FaultString = r.FaultString
	m.Reason = r.Reason
	m.DurationMs = r.DurationMs
	m.SessionDurationSec = r.SessionDurationSec
	m.RPCCount = int64(r.RPCCount)
	return m
}
