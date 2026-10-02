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
package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/orc8r/cloud/go/services/entitlements/obsidian/handlers"
	"magma/orc8r/cloud/go/services/entitlements/obsidian/models"
	servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	entitlements_test_init "magma/orc8r/cloud/go/services/entitlements/test_init"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/tenants"
	tenant_protos "magma/orc8r/cloud/go/services/tenants/protos"
	tenants_test_init "magma/orc8r/cloud/go/services/tenants/test_init"
)

var now = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

func setup(t *testing.T, cfg servicers.Config) *echo.Echo {
	tenants_test_init.StartTestService(t)
	entitlements_test_init.StartTestService(t, cfg, func() time.Time { return now })
	_, err := tenants.CreateTenant(context.Background(), 7, &tenant_protos.Tenant{Name: "acme", Networks: []string{"n1"}})
	require.NoError(t, err)
	e := echo.New()
	obsidian.AttachHandlers(e, handlers.GetObsidianHandlers())
	return e
}

func do(e *echo.Echo, method, url, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, url, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestTenantEntitlements(t *testing.T) {
	e := setup(t, servicers.Config{Enforce: true})
	base := "/magma/v1/tenants/7/entitlements"

	rec := do(e, http.MethodGet, base, "")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, "[]", rec.Body.String())
	assert.Equal(t, http.StatusNotFound, do(e, http.MethodGet, base+"/acs", "").Code)

	rec = do(e, http.MethodPut, base+"/acs", `{"enabled": true, "not_after": "2026-09-01T00:00:00Z", "grace_days": 10, "source": "license", "license_id": "L-1", "state": "active"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	rec = do(e, http.MethodGet, base+"/acs", "")
	require.Equal(t, http.StatusOK, rec.Code)
	got := &models.Entitlement{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), got))
	assert.Equal(t, "acs", got.Feature)
	assert.True(t, *got.Enabled)
	assert.Equal(t, int32(10), *got.GraceDays)
	assert.Equal(t, "license", *got.Source)
	assert.Equal(t, "L-1", got.LicenseID)
	assert.Equal(t, "2026-09-01T00:00:00.000Z", got.NotAfter.String())
	assert.Equal(t, now, time.Time(got.UpdatedAt))
	// Expired a month ago with 10 days of grace: the server says frozen,
	// whatever the client sent.
	assert.Equal(t, models.EntitlementState("frozen"), got.State)

	// Defaults: no expiry, 30 days of grace, manual.
	require.Equal(t, http.StatusNoContent, do(e, http.MethodPut, base+"/cbsd", `{"feature": "cbsd", "enabled": false}`).Code)
	rec = do(e, http.MethodGet, base, "")
	var list []*models.Entitlement
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 2)
	assert.Equal(t, "cbsd", list[1].Feature)
	assert.Nil(t, list[1].NotAfter)
	assert.Equal(t, int32(30), *list[1].GraceDays)
	assert.Equal(t, "manual", *list[1].Source)
	assert.Equal(t, models.EntitlementState("disabled"), list[1].State)
	assert.NotContains(t, rec.Body.String(), "1970")

	// PUT on the collection replaces it.
	require.Equal(t, http.StatusNoContent, do(e, http.MethodPut, base, `[{"feature": "acs", "enabled": true}]`).Code)
	rec = do(e, http.MethodGet, base, "")
	list = nil
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, models.EntitlementState("active"), list[0].State)

	require.Equal(t, http.StatusNoContent, do(e, http.MethodDelete, base+"/acs", "").Code)
	assert.Equal(t, http.StatusNotFound, do(e, http.MethodGet, base+"/acs", "").Code)
}

func TestTenantEntitlementsRejectBadInput(t *testing.T) {
	e := setup(t, servicers.Config{})
	base := "/magma/v1/tenants/7/entitlements"
	for name, tc := range map[string]struct{ url, body string }{
		"feature mismatch": {base + "/acs", `{"feature": "cbsd", "enabled": true}`},
		"malformed":        {base + "/acs", `{"enabled": "yes"`},
		"bad source":       {base + "/acs", `{"enabled": true, "source": "pirated"}`},
		"negative grace":   {base + "/acs", `{"enabled": true, "grace_days": -1}`},
		"bad feature":      {base + "/ACS", `{"enabled": true}`},
		"duplicate":        {base, `[{"feature": "acs", "enabled": true}, {"feature": "acs", "enabled": false}]`},
		"null item":        {base, `[null]`},
		"no enabled":       {base + "/acs", `{"feature": "acs"}`},
		"bad tenant":       {"/magma/v1/tenants/x/entitlements/acs", `{"enabled": true}`},
	} {
		rec := do(e, http.MethodPut, tc.url, tc.body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	}
}

func TestNetworkEntitlements(t *testing.T) {
	e := setup(t, servicers.Config{Enforce: true})
	require.Equal(t, http.StatusNoContent, do(e, http.MethodPut, "/magma/v1/tenants/7/entitlements/acs", `{"enabled": true}`).Code)

	rec := do(e, http.MethodGet, "/magma/v1/networks/n1/entitlements", "")
	require.Equal(t, http.StatusOK, rec.Code)
	got := &models.NetworkEntitlements{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), got))
	assert.Equal(t, "n1", got.NetworkID)
	assert.True(t, got.Enforce)
	assert.Equal(t, int64(7), *got.TenantID)
	require.Len(t, got.Entitlements, 1)
	assert.Equal(t, models.EntitlementState("active"), got.Entitlements[0].State)

	rec = do(e, http.MethodGet, "/magma/v1/networks/orphan/entitlements", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"network_id": "orphan", "enforce": true, "entitlements": []}`, rec.Body.String())
}
