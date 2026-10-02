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
	"errors"
	"testing"

	"github.com/go-openapi/swag"
	"github.com/stretchr/testify/assert"

	"magma/lte/cloud/go/lte"
	lte_mconfig "magma/lte/cloud/go/protos/mconfig"
	lte_models "magma/lte/cloud/go/services/lte/obsidian/models"
	"magma/orc8r/cloud/go/services/configurator"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/test_utils"
	"magma/orc8r/lib/go/protos"
)

func stubAcsEntitlement(t *testing.T, d entitlements.Decision, err error) {
	saved := acsEntitlement
	t.Cleanup(func() { acsEntitlement = saved })
	acsEntitlement = func(_ context.Context, networkID string) (entitlements.Decision, error) {
		assert.Equal(t, "n1", networkID)
		return d, err
	}
}

func network(cfg *lte_models.NetworkAcsConfigs) *configurator.Network {
	n := &configurator.Network{ID: "n1", Configs: map[string]interface{}{}}
	if cfg != nil {
		n.Configs[lte.AcsNetworkConfigType] = cfg
	}
	return n
}

var (
	notEnforced = entitlements.Decision{State: entitlements.StateDisabled}
	active      = entitlements.Decision{Enforced: true, State: entitlements.StateActive}
	grace       = entitlements.Decision{Enforced: true, State: entitlements.StateGrace}
	frozen      = entitlements.Decision{Enforced: true, State: entitlements.StateFrozen}
	disabled    = entitlements.Decision{Enforced: true, State: entitlements.StateDisabled}
)

func TestAcsdMconfigDefaults(t *testing.T) {
	stubAcsEntitlement(t, notEnforced, nil)
	test_utils.AssertMessagesEqual(t, &lte_mconfig.AcsD{LogLevel: protos.LogLevel_INFO, Mode: lte_mconfig.AcsD_ACTIVE}, getAcsdMconfig(context.Background(), network(nil)))
	test_utils.AssertMessagesEqual(t, &lte_mconfig.AcsD{LogLevel: protos.LogLevel_INFO}, getAcsdMconfig(context.Background(), network(&lte_models.NetworkAcsConfigs{})))
}

func TestAcsdMconfigFromNetworkConfig(t *testing.T) {
	stubAcsEntitlement(t, active, nil)
	cfg := &lte_models.NetworkAcsConfigs{
		Mode:                   swag.String(lte_models.AcsModeActive),
		PeriodicInformInterval: swag.Int32(300),
		Port:                   swag.Int32(7547),
	}
	test_utils.AssertMessagesEqual(t, &lte_mconfig.AcsD{
		LogLevel: protos.LogLevel_INFO, Mode: lte_mconfig.AcsD_ACTIVE,
		PeriodicInformInterval: 300, Port: 7547,
	}, getAcsdMconfig(context.Background(), network(cfg)))

	// The operator can freeze acsd by hand.
	cfg.Mode = swag.String(lte_models.AcsModeFrozen)
	assert.Equal(t, lte_mconfig.AcsD_FROZEN, getAcsdMconfig(context.Background(), network(cfg)).Mode)
}

func TestAcsdMconfigFollowsEntitlement(t *testing.T) {
	cfg := &lte_models.NetworkAcsConfigs{PeriodicInformInterval: swag.Int32(60)}
	for name, tc := range map[string]struct {
		d    entitlements.Decision
		err  error
		want lte_mconfig.AcsD_Mode
	}{
		"not enforced":   {notEnforced, nil, lte_mconfig.AcsD_ACTIVE},
		"active":         {active, nil, lte_mconfig.AcsD_ACTIVE},
		"grace":          {grace, nil, lte_mconfig.AcsD_ACTIVE},
		"past grace":     {frozen, nil, lte_mconfig.AcsD_FROZEN},
		"not entitled":   {disabled, nil, lte_mconfig.AcsD_ACTIVE},
		"lookup failure": {entitlements.Decision{}, errors.New("down"), lte_mconfig.AcsD_ACTIVE},
		"frozen, not enforced": {
			entitlements.Decision{State: entitlements.StateFrozen}, nil, lte_mconfig.AcsD_ACTIVE,
		},
	} {
		stubAcsEntitlement(t, tc.d, tc.err)
		got := getAcsdMconfig(context.Background(), network(cfg))
		assert.Equal(t, tc.want, got.Mode, name)
		assert.Equal(t, int32(60), got.PeriodicInformInterval, name)
	}
}
