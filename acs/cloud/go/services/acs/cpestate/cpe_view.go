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

// Package cpestate types the cpe_acs state acsd reports for each CPE
// (lte/gateway/python/magma/acsd/cpe_state.py CpeView).
package cpestate

import (
	"context"
	"encoding/json"
	"errors"

	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/serde"
	"magma/orc8r/cloud/go/services/state"
)

const (
	ModeCore    = "core"
	ModeClaimed = "claimed"
)

// Serdes reads cpe_acs typed; lte registers it as untyped JSON.
var Serdes = serde.NewRegistry(state.NewStateSerde(lte.CPEAcsStateType, &CpeView{}))

// CpeView is acsd's view of a CPE; times are Unix seconds, 0 when unset.
type CpeView struct {
	CpeKey          string                 `json:"cpe_key"`
	Mode            string                 `json:"mode"`
	Imsi            string                 `json:"imsi"`
	SerialNumber    string                 `json:"serial_number"`
	Oui             string                 `json:"oui"`
	ProductClass    string                 `json:"product_class"`
	SoftwareVersion string                 `json:"software_version"`
	Handler         string                 `json:"handler"`
	LastInform      float64                `json:"last_inform"`
	InformsTotal    int64                  `json:"informs_total"`
	Online          bool                   `json:"online"`
	PendingTasks    int32                  `json:"pending_tasks"`
	LastSession     *CpeSession            `json:"last_session"`
	Model           map[string]interface{} `json:"model"`
}

type CpeSession struct {
	SessionID   string  `json:"session_id"`
	Result      string  `json:"result"`
	Reason      string  `json:"reason"`
	Started     float64 `json:"started"`
	Ended       float64 `json:"ended"`
	TasksDone   int32   `json:"tasks_done"`
	TasksFailed int32   `json:"tasks_failed"`
	Faults      int32   `json:"faults"`
}

func (m *CpeView) MarshalBinary() ([]byte, error) {
	return json.Marshal(m)
}

func (m *CpeView) UnmarshalBinary(b []byte) error {
	return json.Unmarshal(b, m)
}

func (m *CpeView) ValidateModel(context.Context) error {
	if m.CpeKey == "" {
		return errors.New("cpe_key must be set")
	}
	return nil
}

// ModelName is the ModelName the CPE reported, from the normalized model.
func (m *CpeView) ModelName() string {
	identity, _ := m.Model["identity"].(map[string]interface{})
	name, _ := identity["model_name"].(string)
	return name
}
