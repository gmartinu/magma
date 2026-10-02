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
package servicers

import (
	"context"

	"github.com/go-openapi/swag"
	"github.com/golang/glog"

	"magma/lte/cloud/go/lte"
	lte_mconfig "magma/lte/cloud/go/protos/mconfig"
	lte_models "magma/lte/cloud/go/services/lte/obsidian/models"
	"magma/orc8r/cloud/go/services/configurator"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/lib/go/protos"
)

// acsEntitlement resolves the acs entitlement of a network; a variable so
// tests can stub the entitlements service.
var acsEntitlement = func(ctx context.Context, networkID string) (entitlements.Decision, error) {
	return entitlements.ForNetwork(ctx, networkID, entitlements.FeatureACS)
}

// getAcsdMconfig builds acsd's mconfig from the network's ACS config. An
// acs entitlement expired past its grace period forces FROZEN. When the
// entitlement cannot be read the configured mode stands: a control-plane
// outage must not change what the gateway does. A network not entitled
// at all still gets the mconfig; magmad does not run acsd there (see the
// orchestrator builder).
func getAcsdMconfig(ctx context.Context, network *configurator.Network) *lte_mconfig.AcsD {
	ret := &lte_mconfig.AcsD{LogLevel: protos.LogLevel_INFO, Mode: lte_mconfig.AcsD_ACTIVE}
	if cfg, ok := network.Configs[lte.AcsNetworkConfigType].(*lte_models.NetworkAcsConfigs); ok && cfg != nil {
		if swag.StringValue(cfg.Mode) == lte_models.AcsModeFrozen {
			ret.Mode = lte_mconfig.AcsD_FROZEN
		}
		ret.PeriodicInformInterval = swag.Int32Value(cfg.PeriodicInformInterval)
		ret.Port = swag.Int32Value(cfg.Port)
	}
	d, err := acsEntitlement(ctx, network.ID)
	switch {
	case err != nil:
		glog.Warningf("acsd mconfig for network %s: cannot read the acs entitlement, keeping mode %s: %v", network.ID, ret.Mode, err)
	case d.Frozen():
		ret.Mode = lte_mconfig.AcsD_FROZEN
	}
	return ret
}
