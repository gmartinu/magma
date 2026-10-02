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
package entitlements_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/protos"
)

const day = int64(24 * 60 * 60)

func TestState(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	at := now.Unix()
	cases := map[string]struct {
		e    *protos.Entitlement
		want string
	}{
		"none":            {nil, entitlements.StateDisabled},
		"switched off":    {&protos.Entitlement{Enabled: false}, entitlements.StateDisabled},
		"no expiry":       {&protos.Entitlement{Enabled: true}, entitlements.StateActive},
		"before expiry":   {&protos.Entitlement{Enabled: true, NotAfter: at + 1}, entitlements.StateActive},
		"at expiry":       {&protos.Entitlement{Enabled: true, NotAfter: at}, entitlements.StateActive},
		"in grace":        {&protos.Entitlement{Enabled: true, NotAfter: at - day, GraceDays: 30}, entitlements.StateGrace},
		"last grace sec":  {&protos.Entitlement{Enabled: true, NotAfter: at - 30*day, GraceDays: 30}, entitlements.StateGrace},
		"past grace":      {&protos.Entitlement{Enabled: true, NotAfter: at - 30*day - 1, GraceDays: 30}, entitlements.StateFrozen},
		"no grace":        {&protos.Entitlement{Enabled: true, NotAfter: at - 1}, entitlements.StateFrozen},
		"off and expired": {&protos.Entitlement{Enabled: false, NotAfter: at - 100*day}, entitlements.StateDisabled},
	}
	for name, tc := range cases {
		assert.Equal(t, tc.want, entitlements.State(tc.e, now), name)
	}
}

func TestDecide(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	frozen := &protos.Entitlement{Feature: "acs", Enabled: true, NotAfter: now.Unix() - 1}
	other := &protos.Entitlement{Feature: "other", Enabled: true}
	tenant := func(enforce bool, ents ...*protos.Entitlement) *protos.NetworkEntitlements {
		return &protos.NetworkEntitlements{HasTenant: true, Enforce: enforce, Entitlements: ents}
	}

	d := entitlements.Decide(tenant(true, other, frozen), "acs", now)
	assert.Equal(t, entitlements.Decision{Enforced: true, State: entitlements.StateFrozen}, d)
	assert.True(t, d.Frozen())
	assert.False(t, d.Allowed())
	assert.False(t, d.Denied())

	d = entitlements.Decide(tenant(true, other), "acs", now)
	assert.True(t, d.Denied())
	assert.False(t, d.Allowed())

	d = entitlements.Decide(tenant(true, &protos.Entitlement{Feature: "acs", Enabled: true}), "acs", now)
	assert.True(t, d.Allowed())

	// Not enforcing: the state is reported but nothing is restricted.
	d = entitlements.Decide(tenant(false), "acs", now)
	assert.Equal(t, entitlements.StateDisabled, d.State)
	assert.True(t, d.Allowed())
	assert.False(t, d.Denied())
	assert.False(t, d.Frozen())
	d = entitlements.Decide(tenant(false, frozen), "acs", now)
	assert.True(t, d.Allowed())
	assert.False(t, d.Frozen())

	// No tenant lists the network.
	assert.True(t, entitlements.Decide(&protos.NetworkEntitlements{Enforce: true}, "acs", now).Denied())
	assert.True(t, entitlements.Decide(&protos.NetworkEntitlements{Enforce: true, AllowUntenanted: true}, "acs", now).Allowed())
	assert.True(t, entitlements.Decide(nil, "acs", now).Allowed())
}
