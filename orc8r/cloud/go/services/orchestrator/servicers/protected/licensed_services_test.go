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
	"errors"
	"testing"

	"github.com/go-openapi/swag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/configurator"
	"magma/orc8r/cloud/go/services/entitlements"
	entitlement_protos "magma/orc8r/cloud/go/services/entitlements/protos"
	entitlement_servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	entitlements_test_init "magma/orc8r/cloud/go/services/entitlements/test_init"
	"magma/orc8r/cloud/go/services/orchestrator/obsidian/models"
	servicers "magma/orc8r/cloud/go/services/orchestrator/servicers/protected"
	orchestrator_test_init "magma/orc8r/cloud/go/services/orchestrator/test_init"
	"magma/orc8r/cloud/go/services/tenants"
	tenant_protos "magma/orc8r/cloud/go/services/tenants/protos"
	tenants_test_init "magma/orc8r/cloud/go/services/tenants/test_init"
	mconfig_protos "magma/orc8r/lib/go/protos/mconfig"
)

func TestFilterLicensedServices(t *testing.T) {
	services := []string{"monitord", "acsd", "td-agent-bit"}
	for name, tc := range map[string]struct {
		d    entitlements.Decision
		err  error
		want []string
	}{
		"not enforced": {entitlements.Decision{State: entitlements.StateDisabled}, nil, services},
		"active":       {entitlements.Decision{Enforced: true, State: entitlements.StateActive}, nil, services},
		"grace":        {entitlements.Decision{Enforced: true, State: entitlements.StateGrace}, nil, services},
		// Frozen acsd keeps running, read-only.
		"frozen":         {entitlements.Decision{Enforced: true, State: entitlements.StateFrozen}, nil, services},
		"not entitled":   {entitlements.Decision{Enforced: true, State: entitlements.StateDisabled}, nil, []string{"monitord", "td-agent-bit"}},
		"lookup failure": {entitlements.Decision{}, errors.New("down"), services},
	} {
		calls := 0
		restore := servicers.StubFeatureDecision(func(_ context.Context, networkID, feature string) (entitlements.Decision, error) {
			calls++
			assert.Equal(t, "n1", networkID)
			assert.Equal(t, entitlements.FeatureACS, feature)
			return tc.d, tc.err
		})
		assert.Equal(t, tc.want, servicers.FilterLicensedServices(context.Background(), "n1", services), name)
		assert.Equal(t, 1, calls, name)
		restore()
	}
}

func TestFilterLicensedServicesSkipsLookupWithoutLicensedServices(t *testing.T) {
	defer servicers.StubFeatureDecision(func(context.Context, string, string) (entitlements.Decision, error) {
		t.Fatal("no licensed service: no lookup expected")
		return entitlements.Decision{}, nil
	})()
	assert.Equal(t, []string{"monitord"}, servicers.FilterLicensedServices(context.Background(), "n1", []string{"monitord"}))
	assert.Nil(t, servicers.FilterLicensedServices(context.Background(), "n1", nil))
}

func buildDynamicServices(t *testing.T) []string {
	nw := configurator.Network{ID: "n1"}
	gw := configurator.NetworkEntity{
		Type: orc8r.MagmadGatewayType, Key: "gw1",
		Config: &models.MagmadGatewayConfigs{
			AutoupgradeEnabled: swag.Bool(false), CheckinInterval: 60, CheckinTimeout: 10,
			DynamicServices: []string{"monitord", "acsd"},
		},
	}
	actual, err := buildBaseOrchestrator(&nw, &configurator.EntityGraph{Entities: []configurator.NetworkEntity{gw}}, "gw1")
	require.NoError(t, err)
	return actual["magmad"].(*mconfig_protos.MagmaD).DynamicServices
}

func startEntitlements(t *testing.T, enforce bool, ents ...*entitlement_protos.Entitlement) {
	orchestrator_test_init.StartTestService(t)
	tenants_test_init.StartTestService(t)
	entitlements_test_init.StartTestService(t, entitlement_servicers.Config{Enforce: enforce}, nil)
	_, err := tenants.CreateTenant(context.Background(), 1, &tenant_protos.Tenant{Networks: []string{"n1"}})
	require.NoError(t, err)
	require.NoError(t, entitlements.SetEntitlements(context.Background(), 1, ents, true))
}

func TestBuild_DropsAcsdWhenNotEntitled(t *testing.T) {
	startEntitlements(t, true, &entitlement_protos.Entitlement{Feature: entitlements.FeatureACS, Enabled: false})
	assert.Equal(t, []string{"monitord"}, buildDynamicServices(t))
}

func TestBuild_KeepsAcsdWhenEntitled(t *testing.T) {
	startEntitlements(t, true, &entitlement_protos.Entitlement{Feature: entitlements.FeatureACS, Enabled: true})
	assert.Equal(t, []string{"monitord", "acsd"}, buildDynamicServices(t))
}

func TestBuild_KeepsAcsdWhenNotEnforced(t *testing.T) {
	startEntitlements(t, false)
	assert.Equal(t, []string{"monitord", "acsd"}, buildDynamicServices(t))
}
