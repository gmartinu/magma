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

// Package entitlements says which licensed features a tenant (NMS
// organization) may use. With enforcement off (the default) every feature
// counts as entitled, so deployments that do not sell features behave as
// before.
package entitlements

import (
	"time"

	"magma/orc8r/cloud/go/services/entitlements/protos"
)

const (
	ServiceName      = "entitlements"
	DBTableName      = "entitlements"
	EntitlementType  = "entitlement"
	DefaultGraceDays = 30

	SourceManual  = "manual"
	SourceLicense = "license"

	// FeatureACS is the TR-069 ACS (acsd on the AGW, the acs REST API).
	FeatureACS = "acs"
)

// States of an entitlement, from most to least usable.
const (
	// StateActive: enabled and not expired.
	StateActive = "active"
	// StateGrace: expired, but within grace_days; nothing changes yet.
	StateGrace = "grace"
	// StateFrozen: expired past the grace period. REST is read-only and
	// gateway services run frozen: they keep answering devices but change
	// nothing on them.
	StateFrozen = "frozen"
	// StateDisabled: no entitlement, or switched off. Gateway services do
	// not run and REST refuses every call.
	StateDisabled = "disabled"
)

// DynamicServiceFeatures maps the gateway dynamic services that belong to
// a licensed feature to that feature.
var DynamicServiceFeatures = map[string]string{
	"acsd": FeatureACS,
}

// State is the effective state of an entitlement at now; nil is disabled.
func State(e *protos.Entitlement, now time.Time) string {
	if e == nil || !e.Enabled {
		return StateDisabled
	}
	if e.NotAfter == 0 || now.Unix() <= e.NotAfter {
		return StateActive
	}
	graceEnd := e.NotAfter + int64(e.GraceDays)*int64(24*time.Hour/time.Second)
	if now.Unix() <= graceEnd {
		return StateGrace
	}
	return StateFrozen
}

// Decision is what an entitlement means for one feature on one network.
type Decision struct {
	// Enforced is false when the deployment does not enforce entitlements.
	Enforced bool
	// State is the entitlement's state, as if enforced.
	State string
}

// Allowed reports whether the feature may run with no restriction.
func (d Decision) Allowed() bool {
	return !d.Enforced || d.State == StateActive || d.State == StateGrace
}

// Frozen reports whether the feature must run read-only.
func (d Decision) Frozen() bool {
	return d.Enforced && d.State == StateFrozen
}

// Denied reports whether the feature must not run at all.
func (d Decision) Denied() bool {
	return d.Enforced && d.State == StateDisabled
}

// Decide is the decision for feature from a network's entitlements.
func Decide(ne *protos.NetworkEntitlements, feature string, now time.Time) Decision {
	d := Decision{Enforced: ne.GetEnforce(), State: StateDisabled}
	if !ne.GetHasTenant() {
		if ne.GetAllowUntenanted() {
			d.State = StateActive
		}
		return d
	}
	for _, e := range ne.GetEntitlements() {
		if e.Feature == feature {
			d.State = State(e, now)
			break
		}
	}
	return d
}
