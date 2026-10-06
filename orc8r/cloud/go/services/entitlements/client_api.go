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

package entitlements

import (
	"context"
	"sync"
	"time"

	"github.com/golang/glog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"magma/orc8r/cloud/go/services/entitlements/protos"
	"magma/orc8r/lib/go/merrors"
	lib_protos "magma/orc8r/lib/go/protos"
	"magma/orc8r/lib/go/registry"
)

func getClient() (protos.EntitlementsClient, error) {
	conn, err := registry.GetConnection(ServiceName, lib_protos.ServiceType_PROTECTED)
	if err != nil {
		initErr := merrors.NewInitError(err, ServiceName)
		glog.Error(initErr)
		return nil, initErr
	}
	return protos.NewEntitlementsClient(conn), nil
}

// ListEntitlements returns the entitlements of a tenant.
func ListEntitlements(ctx context.Context, tenantID int64) ([]*protos.Entitlement, error) {
	client, err := getClient()
	if err != nil {
		return nil, err
	}
	res, err := client.ListEntitlements(ctx, &protos.TenantRequest{TenantId: tenantID})
	if err != nil {
		return nil, mapErr(err)
	}
	return res.Entitlements, nil
}

// GetEntitlement returns merrors.ErrNotFound when the tenant has none.
func GetEntitlement(ctx context.Context, tenantID int64, feature string) (*protos.Entitlement, error) {
	client, err := getClient()
	if err != nil {
		return nil, err
	}
	res, err := client.GetEntitlement(ctx, &protos.GetEntitlementRequest{TenantId: tenantID, Feature: feature})
	return res, mapErr(err)
}

// SetEntitlements creates or updates the given entitlements of a tenant;
// replace also removes the ones not given.
func SetEntitlements(ctx context.Context, tenantID int64, ents []*protos.Entitlement, replace bool) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	_, err = client.SetEntitlements(ctx, &protos.SetEntitlementsRequest{
		TenantId: tenantID, Entitlements: ents, Replace: replace,
	})
	return mapErr(err)
}

// DeleteEntitlement removes one entitlement of a tenant.
func DeleteEntitlement(ctx context.Context, tenantID int64, feature string) error {
	client, err := getClient()
	if err != nil {
		return err
	}
	_, err = client.DeleteEntitlement(ctx, &protos.GetEntitlementRequest{TenantId: tenantID, Feature: feature})
	return mapErr(err)
}

// GetNetworkEntitlements returns the entitlements of the network's tenant.
func GetNetworkEntitlements(ctx context.Context, networkID string) (*protos.NetworkEntitlements, error) {
	client, err := getClient()
	if err != nil {
		return nil, err
	}
	res, err := client.GetNetworkEntitlements(ctx, &protos.NetworkRequest{NetworkId: networkID})
	return res, mapErr(err)
}

// LookupTimeout bounds an entitlement lookup, so a hung entitlements
// service cannot stall an mconfig build or a REST call.
const LookupTimeout = 5 * time.Second

// lastKnown keeps the last NetworkEntitlements read for each network, so
// an outage of the entitlements service falls back to what was last
// decided instead of to "allowed". It holds the entitlements, not the
// decision, so an entitlement that expires during the outage still
// expires.
var lastKnown sync.Map

// fetchNetworkEntitlements is GetNetworkEntitlements; a variable so tests
// can take the service down.
var fetchNetworkEntitlements = GetNetworkEntitlements

// ForNetwork is the decision for feature on a network now. When the
// entitlements service cannot answer, it is the decision from the last
// entitlements this process read for the network; it returns the error
// only when there are none.
func ForNetwork(ctx context.Context, networkID, feature string) (Decision, error) {
	ctx, cancel := context.WithTimeout(ctx, LookupTimeout)
	defer cancel()
	ne, err := fetchNetworkEntitlements(ctx, networkID)
	if err == nil {
		lastKnown.Store(networkID, ne)
		return Decide(ne, feature, time.Now()), nil
	}
	cached, ok := lastKnown.Load(networkID)
	if !ok {
		return Decision{}, err
	}
	glog.Warningf("Cannot read the entitlements of network %s, using the last known ones: %v", networkID, err)
	return Decide(cached.(*protos.NetworkEntitlements), feature, time.Now()), nil
}

// ForgetLastKnown drops the cached entitlements; for tests.
func ForgetLastKnown() {
	lastKnown.Range(func(k, _ interface{}) bool {
		lastKnown.Delete(k)
		return true
	})
}

func mapErr(err error) error {
	if err != nil && status.Code(err) == codes.NotFound {
		return merrors.ErrNotFound
	}
	return err
}
