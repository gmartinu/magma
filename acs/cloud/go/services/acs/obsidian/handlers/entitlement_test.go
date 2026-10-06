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

package handlers

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/protos"
	entitlements_servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	"magma/orc8r/cloud/go/services/tenants"
	tenant_protos "magma/orc8r/cloud/go/services/tenants/protos"
	tenants_test_init "magma/orc8r/cloud/go/services/tenants/test_init"
)

// entitledStatuses puts n1 under a tenant whose acs entitlement is
// enabled or not, then returns the status of a CPE read and a task write.
func entitledStatuses(t *testing.T, enforce, enabled bool) (read, write int) {
	tenants_test_init.StartTestService(t)
	setupNetworkWithEntitlements(t, entitlements_servicers.Config{Enforce: enforce})
	ctx := context.Background()
	_, err := tenants.CreateTenant(ctx, 1, &tenant_protos.Tenant{Networks: []string{"n1"}})
	require.NoError(t, err)
	ent := &protos.Entitlement{Feature: entitlements.FeatureACS, Enabled: enabled, NotAfter: time.Now().Add(30 * 24 * time.Hour).Unix()}
	require.NoError(t, entitlements.SetEntitlements(ctx, 1, []*protos.Entitlement{ent}, true))

	reportCpe(t, "hw2", titan(cpeKey, cpestate.ModeCore, true))
	acsd := &fakeAcsd{task: rebootTask()}
	h := newHandlers(acsd)
	read = serve(t, h, http.MethodGet, CpesPath, "/magma/v1/acs/n1/cpes", map[string]string{"network_id": "n1"}, "").Code
	write = serve(t, h, http.MethodPost, TasksPath, "/", cpeParams(), `{"type": "reboot", "max_attempts": 3}`).Code
	return read, write
}

func TestEntitledNetworkPasses(t *testing.T) {
	read, write := entitledStatuses(t, true, true)
	assert.Equal(t, http.StatusOK, read)
	assert.Equal(t, http.StatusCreated, write)
}

func TestNotEntitledNetworkIsForbidden(t *testing.T) {
	read, write := entitledStatuses(t, true, false)
	assert.Equal(t, http.StatusForbidden, read)
	assert.Equal(t, http.StatusForbidden, write)
}

func TestNotEnforcedPassesADisabledEntitlement(t *testing.T) {
	read, write := entitledStatuses(t, false, false)
	assert.Equal(t, http.StatusOK, read)
	assert.Equal(t, http.StatusCreated, write)
}
