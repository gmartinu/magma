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
package guard_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/obsidian/guard"
	"magma/orc8r/cloud/go/services/entitlements/protos"
	servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	entitlements_test_init "magma/orc8r/cloud/go/services/entitlements/test_init"
	"magma/orc8r/cloud/go/services/tenants"
	tenant_protos "magma/orc8r/cloud/go/services/tenants/protos"
	tenants_test_init "magma/orc8r/cloud/go/services/tenants/test_init"
)

const day = int64(24 * 60 * 60)

var methods = []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete}

// serve runs one request through an echo route guarded for "acs" and
// returns the status, 200 when the handler ran.
func serve(method, networkID string) int {
	e := echo.New()
	ok := func(c echo.Context) error { return c.NoContent(http.StatusOK) }
	e.Add(method, "/magma/v1/acs/:network_id/cpes", ok, guard.RequireEntitlement(entitlements.FeatureACS))
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(method, "/magma/v1/acs/"+networkID+"/cpes", nil))
	return rec.Code
}

func statuses(networkID string) map[string]int {
	ret := map[string]int{}
	for _, m := range methods {
		ret[m] = serve(m, networkID)
	}
	return ret
}

func all(code int) map[string]int {
	return map[string]int{http.MethodGet: code, http.MethodHead: code, http.MethodPost: code, http.MethodPut: code, http.MethodDelete: code}
}

func readOnly(writeCode int) map[string]int {
	return map[string]int{http.MethodGet: 200, http.MethodHead: 200, http.MethodPost: writeCode, http.MethodPut: writeCode, http.MethodDelete: writeCode}
}

func start(t *testing.T, cfg servicers.Config) {
	tenants_test_init.StartTestService(t)
	entitlements_test_init.StartTestService(t, cfg, nil)
	ctx := context.Background()
	_, err := tenants.CreateTenant(ctx, 2, &tenant_protos.Tenant{Networks: []string{"other"}})
	require.NoError(t, err)
	now := time.Now().Unix()
	// One tenant per state: the guard reads the tenant of the network.
	for id, e := range []*protos.Entitlement{
		{Feature: "acs", Enabled: true, NotAfter: now + 30*day},
		{Feature: "acs", Enabled: true, NotAfter: now - day, GraceDays: 30},
		{Feature: "acs", Enabled: true, NotAfter: now - 31*day, GraceDays: 30},
		{Feature: "acs", Enabled: false},
	} {
		network := []string{"active", "grace", "frozen", "off"}[id]
		id := int64(100 + id)
		_, err := tenants.CreateTenant(ctx, id, &tenant_protos.Tenant{Networks: []string{network + "-net"}})
		require.NoError(t, err)
		require.NoError(t, entitlements.SetEntitlements(ctx, id, []*protos.Entitlement{e}, true))
	}
}

func TestEnforced(t *testing.T) {
	start(t, servicers.Config{Enforce: true})
	assert.Equal(t, all(200), statuses("active-net"))
	assert.Equal(t, all(200), statuses("grace-net"))
	assert.Equal(t, readOnly(http.StatusForbidden), statuses("frozen-net"))
	assert.Equal(t, all(http.StatusForbidden), statuses("off-net"))
	// A tenant with no acs record, and a network no tenant lists.
	assert.Equal(t, all(http.StatusForbidden), statuses("other"))
	assert.Equal(t, all(http.StatusForbidden), statuses("orphan"))
}

func TestNotEnforcedIsPassthrough(t *testing.T) {
	start(t, servicers.Config{Enforce: false})
	for _, n := range []string{"active-net", "grace-net", "frozen-net", "off-net", "other", "orphan"} {
		assert.Equal(t, all(200), statuses(n), n)
	}
}

func TestAllowUntenanted(t *testing.T) {
	start(t, servicers.Config{Enforce: true, AllowUntenanted: true})
	assert.Equal(t, all(200), statuses("orphan"))
	assert.Equal(t, all(http.StatusForbidden), statuses("other"))
}

func TestLookupFailure(t *testing.T) {
	saved := guard.ForNetwork
	defer func() { guard.ForNetwork = saved }()
	guard.ForNetwork = func(context.Context, string, string) (entitlements.Decision, error) {
		return entitlements.Decision{}, errors.New("unreachable")
	}
	assert.Equal(t, readOnly(http.StatusServiceUnavailable), statuses("n1"))
}

func TestCheckEntitlementNeedsNetwork(t *testing.T) {
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())
	err := guard.CheckEntitlement(c, entitlements.FeatureACS)
	var herr *echo.HTTPError
	require.True(t, errors.As(err, &herr))
	assert.Equal(t, http.StatusBadRequest, herr.Code)
}
