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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"magma/orc8r/cloud/go/services/entitlements/protos"
	servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	"magma/orc8r/cloud/go/services/entitlements/servicers/storage"
	tenant_protos "magma/orc8r/cloud/go/services/tenants/protos"
	"magma/orc8r/cloud/go/test_utils"
)

var now = time.Unix(1_700_000_000, 0)

func tenantsOf(networks map[int64][]string) servicers.TenantLister {
	return func(context.Context) (*tenant_protos.TenantList, error) {
		list := &tenant_protos.TenantList{}
		for id, nets := range networks {
			list.Tenants = append(list.Tenants, &tenant_protos.IDAndTenant{Id: id, Tenant: &tenant_protos.Tenant{Networks: nets}})
		}
		return list, nil
	}
}

func newServicer(t *testing.T, cfg servicers.Config, lister servicers.TenantLister) protos.EntitlementsServer {
	store := storage.NewBlobstoreStore(test_utils.NewSQLBlobstore(t, "entitlements_servicer_test"))
	return servicers.NewServicer(store, cfg, lister, func() time.Time { return now })
}

func TestSetGetListDelete(t *testing.T) {
	s := newServicer(t, servicers.Config{}, tenantsOf(nil))
	ctx := context.Background()

	_, err := s.GetEntitlement(ctx, &protos.GetEntitlementRequest{TenantId: 1, Feature: "acs"})
	assert.Equal(t, codes.NotFound, status.Code(err))

	_, err = s.SetEntitlements(ctx, &protos.SetEntitlementsRequest{TenantId: 1, Entitlements: []*protos.Entitlement{
		{Feature: "acs", Enabled: true, NotAfter: 1_800_000_000, GraceDays: 7, LicenseId: "L1", Source: "license", UpdatedAt: 5},
		{Feature: "cbsd", Enabled: false},
	}})
	require.NoError(t, err)

	got, err := s.GetEntitlement(ctx, &protos.GetEntitlementRequest{TenantId: 1, Feature: "acs"})
	require.NoError(t, err)
	// updated_at is the service's clock, not the caller's.
	test_utils.AssertMessagesEqual(t, &protos.Entitlement{
		Feature: "acs", Enabled: true, NotAfter: 1_800_000_000, GraceDays: 7,
		Source: "license", LicenseId: "L1", UpdatedAt: now.Unix(),
	}, got)

	list, err := s.ListEntitlements(ctx, &protos.TenantRequest{TenantId: 1})
	require.NoError(t, err)
	assert.Equal(t, []string{"acs", "cbsd"}, features(list.Entitlements))
	assert.Equal(t, "manual", list.Entitlements[1].Source)

	other, err := s.ListEntitlements(ctx, &protos.TenantRequest{TenantId: 2})
	require.NoError(t, err)
	assert.Empty(t, other.Entitlements)

	// Update one without touching the other.
	_, err = s.SetEntitlements(ctx, &protos.SetEntitlementsRequest{TenantId: 1, Entitlements: []*protos.Entitlement{{Feature: "cbsd", Enabled: true}}})
	require.NoError(t, err)
	list, _ = s.ListEntitlements(ctx, &protos.TenantRequest{TenantId: 1})
	assert.Equal(t, []string{"acs", "cbsd"}, features(list.Entitlements))
	assert.True(t, list.Entitlements[1].Enabled)

	// Replace drops what is left out.
	_, err = s.SetEntitlements(ctx, &protos.SetEntitlementsRequest{TenantId: 1, Replace: true, Entitlements: []*protos.Entitlement{{Feature: "cbsd"}}})
	require.NoError(t, err)
	list, _ = s.ListEntitlements(ctx, &protos.TenantRequest{TenantId: 1})
	assert.Equal(t, []string{"cbsd"}, features(list.Entitlements))

	_, err = s.DeleteEntitlement(ctx, &protos.GetEntitlementRequest{TenantId: 1, Feature: "cbsd"})
	require.NoError(t, err)
	list, _ = s.ListEntitlements(ctx, &protos.TenantRequest{TenantId: 1})
	assert.Empty(t, list.Entitlements)

	_, err = s.SetEntitlements(ctx, &protos.SetEntitlementsRequest{TenantId: 1, Replace: true})
	assert.NoError(t, err)
}

func TestSetValidates(t *testing.T) {
	s := newServicer(t, servicers.Config{}, tenantsOf(nil))
	for name, e := range map[string]*protos.Entitlement{
		"empty feature":  {},
		"bad feature":    {Feature: "ACS!"},
		"negative grace": {Feature: "acs", GraceDays: -1},
		"bad source":     {Feature: "acs", Source: "pirated"},
	} {
		_, err := s.SetEntitlements(context.Background(), &protos.SetEntitlementsRequest{TenantId: 1, Entitlements: []*protos.Entitlement{e}})
		assert.Equal(t, codes.InvalidArgument, status.Code(err), name)
	}
	_, err := s.SetEntitlements(context.Background(), &protos.SetEntitlementsRequest{TenantId: 1, Entitlements: []*protos.Entitlement{{Feature: "acs"}, {Feature: "acs"}}})
	assert.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestGetNetworkEntitlements(t *testing.T) {
	ctx := context.Background()
	s := newServicer(t, servicers.Config{Enforce: true}, tenantsOf(map[int64][]string{1: {"n0"}, 2: {"n1", "n2"}}))
	_, err := s.SetEntitlements(ctx, &protos.SetEntitlementsRequest{TenantId: 2, Entitlements: []*protos.Entitlement{{Feature: "acs", Enabled: true}}})
	require.NoError(t, err)

	got, err := s.GetNetworkEntitlements(ctx, &protos.NetworkRequest{NetworkId: "n2"})
	require.NoError(t, err)
	assert.True(t, got.HasTenant)
	assert.True(t, got.Enforce)
	assert.Equal(t, int64(2), got.TenantId)
	assert.Equal(t, []string{"acs"}, features(got.Entitlements))

	got, err = s.GetNetworkEntitlements(ctx, &protos.NetworkRequest{NetworkId: "n0"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.TenantId)
	assert.Empty(t, got.Entitlements)

	got, err = s.GetNetworkEntitlements(ctx, &protos.NetworkRequest{NetworkId: "orphan"})
	require.NoError(t, err)
	assert.False(t, got.HasTenant)
	assert.Equal(t, "orphan", got.NetworkId)
}

func TestTenantsDown(t *testing.T) {
	down := func(context.Context) (*tenant_protos.TenantList, error) { return nil, errors.New("down") }
	req := &protos.NetworkRequest{NetworkId: "n1"}

	// Enforcing, the answer depends on the tenant: fail.
	_, err := newServicer(t, servicers.Config{Enforce: true}, down).GetNetworkEntitlements(context.Background(), req)
	assert.Equal(t, codes.Unavailable, status.Code(err))

	// Not enforcing, nothing depends on it: answer without a tenant.
	got, err := newServicer(t, servicers.Config{}, down).GetNetworkEntitlements(context.Background(), req)
	require.NoError(t, err)
	assert.False(t, got.Enforce)
	assert.False(t, got.HasTenant)
}

func features(ents []*protos.Entitlement) []string {
	ret := []string{}
	for _, e := range ents {
		ret = append(ret, e.Feature)
	}
	return ret
}
