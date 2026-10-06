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
	"math"
	"regexp"
	"sort"
	"sync"
	"time"

	"github.com/golang/glog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/protos"
	"magma/orc8r/cloud/go/services/entitlements/servicers/storage"
	"magma/orc8r/cloud/go/services/tenants"
	tenant_protos "magma/orc8r/cloud/go/services/tenants/protos"
	"magma/orc8r/lib/go/merrors"
)

var featurePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// tenantsTTL is how long the tenant list is reused. Every REST call of a
// gated feature and every mconfig build asks for a network's tenant; a
// tenant edit reaches entitlements at most this late.
const tenantsTTL = 15 * time.Second

// Config is the deployment's entitlements.yml.
type Config struct {
	// Enforce turns entitlements on. Off (the default), every feature
	// counts as entitled.
	Enforce bool `yaml:"enforce"`
	// AllowUntenanted lets a network no tenant lists use every feature
	// while enforcing (e.g. deployments without an NMS).
	AllowUntenanted bool `yaml:"allowUntenanted"`
}

// TenantLister lists the tenants and their networks.
type TenantLister func(ctx context.Context) (*tenant_protos.TenantList, error)

type servicer struct {
	store   storage.Store
	config  Config
	tenants TenantLister
	now     func() time.Time

	mu       sync.Mutex
	cached   *tenant_protos.TenantList
	cachedAt time.Time
}

// NewServicer serves Entitlements from store; tenants defaults to the
// tenants service and now to time.Now.
func NewServicer(store storage.Store, config Config, lister TenantLister, now func() time.Time) protos.EntitlementsServer {
	if lister == nil {
		lister = tenants.GetAllTenants
	}
	if now == nil {
		now = time.Now
	}
	return &servicer{store: store, config: config, tenants: lister, now: now}
}

func (s *servicer) ListEntitlements(_ context.Context, req *protos.TenantRequest) (*protos.EntitlementList, error) {
	ents, err := s.store.List(req.TenantId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "listing entitlements: %v", err)
	}
	return &protos.EntitlementList{Entitlements: ents}, nil
}

func (s *servicer) GetEntitlement(_ context.Context, req *protos.GetEntitlementRequest) (*protos.Entitlement, error) {
	e, err := s.store.Get(req.TenantId, req.Feature)
	switch {
	case err == merrors.ErrNotFound:
		return nil, status.Errorf(codes.NotFound, "tenant %d has no entitlement to %q", req.TenantId, req.Feature)
	case err != nil:
		return nil, status.Errorf(codes.Internal, "getting entitlement: %v", err)
	}
	return e, nil
}

func (s *servicer) SetEntitlements(_ context.Context, req *protos.SetEntitlementsRequest) (*protos.Void, error) {
	seen := map[string]bool{}
	ents := make([]*protos.Entitlement, 0, len(req.Entitlements))
	for _, in := range req.Entitlements {
		e, err := s.normalize(in)
		if err != nil {
			return nil, err
		}
		if seen[e.Feature] {
			return nil, status.Errorf(codes.InvalidArgument, "feature %q given twice", e.Feature)
		}
		seen[e.Feature] = true
		ents = append(ents, e)
	}
	if err := s.store.Set(req.TenantId, ents, req.Replace); err != nil {
		return nil, status.Errorf(codes.Internal, "setting entitlements: %v", err)
	}
	return &protos.Void{}, nil
}

func (s *servicer) normalize(in *protos.Entitlement) (*protos.Entitlement, error) {
	if !featurePattern.MatchString(in.Feature) {
		return nil, status.Errorf(codes.InvalidArgument, "invalid feature %q", in.Feature)
	}
	if in.GraceDays < 0 || in.NotAfter < 0 {
		return nil, status.Error(codes.InvalidArgument, "grace_days and not_after must not be negative")
	}
	e := &protos.Entitlement{
		Feature:   in.Feature,
		Enabled:   in.Enabled,
		NotAfter:  in.NotAfter,
		GraceDays: in.GraceDays,
		Source:    in.Source,
		LicenseId: in.LicenseId,
		UpdatedAt: s.now().Unix(),
	}
	switch e.Source {
	case "":
		e.Source = entitlements.SourceManual
	case entitlements.SourceManual, entitlements.SourceLicense:
	default:
		return nil, status.Errorf(codes.InvalidArgument, "invalid source %q", e.Source)
	}
	return e, nil
}

func (s *servicer) DeleteEntitlement(_ context.Context, req *protos.GetEntitlementRequest) (*protos.Void, error) {
	if err := s.store.Delete(req.TenantId, req.Feature); err != nil {
		return nil, status.Errorf(codes.Internal, "deleting entitlement: %v", err)
	}
	return &protos.Void{}, nil
}

func (s *servicer) GetNetworkEntitlements(ctx context.Context, req *protos.NetworkRequest) (*protos.NetworkEntitlements, error) {
	res := &protos.NetworkEntitlements{
		NetworkId:       req.NetworkId,
		Enforce:         s.config.Enforce,
		AllowUntenanted: s.config.AllowUntenanted,
		Entitlements:    []*protos.Entitlement{},
	}
	list, err := s.listTenants(ctx)
	if err != nil && !s.config.Enforce {
		// Nothing depends on the tenant when not enforcing.
		return res, nil
	}
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "listing tenants: %v", err)
	}
	var owners []int64
	for _, t := range list.GetTenants() {
		for _, n := range t.GetTenant().GetNetworks() {
			if n == req.NetworkId {
				owners = append(owners, t.Id)
				break
			}
		}
	}
	if len(owners) == 0 {
		return res, nil
	}
	sort.Slice(owners, func(i, j int) bool { return owners[i] < owners[j] })
	if len(owners) > 1 {
		glog.Warningf("Network %s is listed by tenants %v: the most restrictive entitlement of each feature applies", req.NetworkId, owners)
	}
	var lists [][]*protos.Entitlement
	for _, id := range owners {
		ents, err := s.store.List(id)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "listing entitlements: %v", err)
		}
		lists = append(lists, ents)
	}
	res.HasTenant, res.TenantId, res.Entitlements = true, owners[0], mostRestrictive(lists)
	return res, nil
}

// listTenants is the tenant list, reused for tenantsTTL and while the
// tenants service is down.
func (s *servicer) listTenants(ctx context.Context) (*tenant_protos.TenantList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cached != nil && s.now().Sub(s.cachedAt) < tenantsTTL {
		return s.cached, nil
	}
	list, err := s.tenants(ctx)
	if err != nil && s.cached != nil {
		glog.Warningf("Listing tenants: %v; using the list from %s", err, s.cachedAt)
		return s.cached, nil
	}
	if err != nil {
		return nil, err
	}
	s.cached, s.cachedAt = list, s.now()
	return list, nil
}

// mostRestrictive merges the entitlements of the tenants that list one
// network: a feature is entitled only if every tenant has it, with the
// terms that end access first. Neither tenant can grant the shared network
// more than the other pays for, and the result does not depend on the
// order the tenants service lists them in.
func mostRestrictive(lists [][]*protos.Entitlement) []*protos.Entitlement {
	if len(lists) == 1 {
		return lists[0]
	}
	byFeature := map[string]*protos.Entitlement{}
	counts := map[string]int{}
	for _, ents := range lists {
		for _, e := range ents {
			counts[e.Feature]++
			if cur, ok := byFeature[e.Feature]; !ok || stricter(e, cur) {
				byFeature[e.Feature] = e
			}
		}
	}
	out := []*protos.Entitlement{}
	for feature, e := range byFeature {
		if counts[feature] == len(lists) {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Feature < out[j].Feature })
	return out
}

// stricter orders entitlements by when they stop allowing writes: disabled
// first, then the earliest end of grace, then the earliest expiry.
func stricter(a, b *protos.Entitlement) bool {
	if a.Enabled != b.Enabled {
		return !a.Enabled
	}
	if fa, fb := freezesAt(a), freezesAt(b); fa != fb {
		return fa < fb
	}
	return expiresAt(a) < expiresAt(b)
}

func expiresAt(e *protos.Entitlement) int64 {
	if e.NotAfter == 0 {
		return math.MaxInt64
	}
	return e.NotAfter
}

func freezesAt(e *protos.Entitlement) int64 {
	if e.NotAfter == 0 {
		return math.MaxInt64
	}
	return e.NotAfter + int64(e.GraceDays)*int64(24*time.Hour/time.Second)
}
