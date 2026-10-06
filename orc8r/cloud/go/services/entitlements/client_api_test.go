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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/protos"
)

func enforced(e *protos.Entitlement) *protos.NetworkEntitlements {
	return &protos.NetworkEntitlements{NetworkId: "n1", HasTenant: true, Enforce: true, Entitlements: []*protos.Entitlement{e}}
}

func TestForNetwork_FallsBackToLastKnown(t *testing.T) {
	entitlements.ForgetLastKnown()
	var ne *protos.NetworkEntitlements
	var fetchErr error
	defer entitlements.StubFetch(func(context.Context, string) (*protos.NetworkEntitlements, error) {
		return ne, fetchErr
	})()

	// Nothing known yet: the error goes through.
	fetchErr = errors.New("down")
	_, err := entitlements.ForNetwork(context.Background(), "n1", entitlements.FeatureACS)
	assert.Error(t, err)

	ne, fetchErr = enforced(&protos.Entitlement{Feature: entitlements.FeatureACS, Enabled: false}), nil
	d, err := entitlements.ForNetwork(context.Background(), "n1", entitlements.FeatureACS)
	require.NoError(t, err)
	assert.True(t, d.Denied())

	// Down again: still denied, not allowed.
	ne, fetchErr = nil, errors.New("down")
	d, err = entitlements.ForNetwork(context.Background(), "n1", entitlements.FeatureACS)
	require.NoError(t, err)
	assert.True(t, d.Denied())

	// Another network has nothing known.
	_, err = entitlements.ForNetwork(context.Background(), "n2", entitlements.FeatureACS)
	assert.Error(t, err)
}

func TestForNetwork_LastKnownStillExpires(t *testing.T) {
	entitlements.ForgetLastKnown()
	expired := time.Now().Add(-time.Hour).Unix()
	calls := 0
	defer entitlements.StubFetch(func(context.Context, string) (*protos.NetworkEntitlements, error) {
		calls++
		if calls == 1 {
			return enforced(&protos.Entitlement{Feature: entitlements.FeatureACS, Enabled: true, NotAfter: expired}), nil
		}
		return nil, errors.New("down")
	})()
	_, err := entitlements.ForNetwork(context.Background(), "n1", entitlements.FeatureACS)
	require.NoError(t, err)
	d, err := entitlements.ForNetwork(context.Background(), "n1", entitlements.FeatureACS)
	require.NoError(t, err)
	assert.True(t, d.Frozen())
}

func TestForNetwork_HasADeadline(t *testing.T) {
	entitlements.ForgetLastKnown()
	defer entitlements.StubFetch(func(ctx context.Context, _ string) (*protos.NetworkEntitlements, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		assert.WithinDuration(t, time.Now().Add(entitlements.LookupTimeout), deadline, time.Second)
		return nil, errors.New("down")
	})()
	_, _ = entitlements.ForNetwork(context.Background(), "n1", entitlements.FeatureACS)
}
