/*
 * Copyright 2026 The Magma Authors.
 *
 * This source code is licensed under the BSD-style license found in the
 * LICENSE file in the root directory of this source tree.
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package models

import (
	"context"

	"github.com/go-openapi/strfmt"

	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/services/configurator"
	orc8rModels "magma/orc8r/cloud/go/services/orchestrator/obsidian/models"
)

// Modes of NetworkAcsConfigs.Mode.
const (
	AcsModeActive = "active"
	AcsModeFrozen = "frozen"
)

func (m *NetworkAcsConfigs) ValidateModel(context.Context) error {
	return m.Validate(strfmt.Default)
}

// GetFromNetwork returns the network's ACS config, or an empty one: acsd
// then runs on its defaults.
func (m *NetworkAcsConfigs) GetFromNetwork(network configurator.Network) interface{} {
	res := orc8rModels.GetNetworkConfig(network, lte.AcsNetworkConfigType)
	if res == nil {
		return &NetworkAcsConfigs{}
	}
	return res
}

func (m *NetworkAcsConfigs) ToUpdateCriteria(network configurator.Network) (configurator.NetworkUpdateCriteria, error) {
	return orc8rModels.GetNetworkConfigUpdateCriteria(network.ID, lte.AcsNetworkConfigType, m), nil
}
