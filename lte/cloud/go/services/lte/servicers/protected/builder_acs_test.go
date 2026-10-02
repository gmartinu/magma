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
package servicers_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-openapi/swag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/lte/cloud/go/lte"
	lte_mconfig "magma/lte/cloud/go/protos/mconfig"
	lte_models "magma/lte/cloud/go/services/lte/obsidian/models"
	lte_test_init "magma/lte/cloud/go/services/lte/test_init"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/configurator"
	"magma/orc8r/cloud/go/services/entitlements"
	entitlement_protos "magma/orc8r/cloud/go/services/entitlements/protos"
	entitlement_servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	entitlements_test_init "magma/orc8r/cloud/go/services/entitlements/test_init"
	"magma/orc8r/cloud/go/services/tenants"
	tenant_protos "magma/orc8r/cloud/go/services/tenants/protos"
	tenants_test_init "magma/orc8r/cloud/go/services/tenants/test_init"
	"magma/orc8r/cloud/go/storage"
	"magma/orc8r/cloud/go/test_utils"
	"magma/orc8r/lib/go/protos"
)

func buildAcsd(t *testing.T, acs *lte_models.NetworkAcsConfigs) *lte_mconfig.AcsD {
	nw := configurator.Network{
		ID:      "n1",
		Configs: map[string]interface{}{lte.CellularNetworkConfigType: lte_models.NewDefaultTDDNetworkConfig()},
	}
	if acs != nil {
		nw.Configs[lte.AcsNetworkConfigType] = acs
	}
	gw := configurator.NetworkEntity{
		Type: orc8r.MagmadGatewayType, Key: "gw1",
		Associations: storage.TKs{{Type: lte.CellularGatewayEntityType, Key: "gw1"}},
	}
	lteGW := configurator.NetworkEntity{
		Type: lte.CellularGatewayEntityType, Key: "gw1",
		Config:             newDefaultGatewayConfig(),
		ParentAssociations: storage.TKs{gw.GetTK()},
	}
	graph := configurator.EntityGraph{
		Entities: []configurator.NetworkEntity{lteGW, gw},
		Edges:    []configurator.GraphEdge{{From: gw.GetTK(), To: lteGW.GetTK()}},
	}
	actual, err := buildNonFederated(&nw, &graph, "gw1")
	require.NoError(t, err)
	acsd, ok := actual["acsd"].(*lte_mconfig.AcsD)
	require.True(t, ok, "no acsd mconfig")
	return acsd
}

func startEntitlements(t *testing.T, enforce bool, notAfter int64) {
	lte_test_init.StartTestService(t)
	tenants_test_init.StartTestService(t)
	entitlements_test_init.StartTestService(t, entitlement_servicers.Config{Enforce: enforce}, nil)
	ctx := context.Background()
	_, err := tenants.CreateTenant(ctx, 1, &tenant_protos.Tenant{Networks: []string{"n1"}})
	require.NoError(t, err)
	require.NoError(t, entitlements.SetEntitlements(ctx, 1, []*entitlement_protos.Entitlement{
		{Feature: entitlements.FeatureACS, Enabled: true, NotAfter: notAfter, GraceDays: 30},
	}, true))
}

func TestBuilder_Build_Acsd(t *testing.T) {
	startEntitlements(t, true, 0)
	acs := &lte_models.NetworkAcsConfigs{PeriodicInformInterval: swag.Int32(300), Port: swag.Int32(7547)}
	test_utils.AssertMessagesEqual(t, &lte_mconfig.AcsD{
		LogLevel: protos.LogLevel_INFO, Mode: lte_mconfig.AcsD_ACTIVE,
		PeriodicInformInterval: 300, Port: 7547,
	}, buildAcsd(t, acs))
	test_utils.AssertMessagesEqual(t, &lte_mconfig.AcsD{LogLevel: protos.LogLevel_INFO}, buildAcsd(t, nil))
}

func TestBuilder_Build_AcsdFrozenPastGrace(t *testing.T) {
	startEntitlements(t, true, time.Now().Add(-31*24*time.Hour).Unix())
	acsd := buildAcsd(t, &lte_models.NetworkAcsConfigs{PeriodicInformInterval: swag.Int32(300)})
	assert.Equal(t, lte_mconfig.AcsD_FROZEN, acsd.Mode)
	assert.Equal(t, int32(300), acsd.PeriodicInformInterval)
}

func TestBuilder_Build_AcsdInGraceStaysActive(t *testing.T) {
	startEntitlements(t, true, time.Now().Add(-24*time.Hour).Unix())
	assert.Equal(t, lte_mconfig.AcsD_ACTIVE, buildAcsd(t, nil).Mode)
}

func TestBuilder_Build_AcsdNotEnforced(t *testing.T) {
	startEntitlements(t, false, time.Now().Add(-365*24*time.Hour).Unix())
	assert.Equal(t, lte_mconfig.AcsD_ACTIVE, buildAcsd(t, nil).Mode)
}
