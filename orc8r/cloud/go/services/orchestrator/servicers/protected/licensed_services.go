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

	"github.com/golang/glog"

	"magma/orc8r/cloud/go/services/entitlements"
)

// featureDecision resolves the entitlement of a network to a feature; a
// variable so tests can stub the entitlements service.
var featureDecision = entitlements.ForNetwork

// filterLicensedServices drops from a gateway's dynamic services the ones
// whose licensed feature its network is not entitled to, so magmad stops
// them. Frozen (expired past grace) services stay: they run read-only and
// keep answering their devices. When the entitlement cannot be read the
// service stays, so an Orc8r outage never stops a gateway service.
func filterLicensedServices(ctx context.Context, networkID string, services []string) []string {
	var ret []string
	decided := map[string]bool{}
	for _, svc := range services {
		feature, licensed := entitlements.DynamicServiceFeatures[svc]
		if !licensed {
			ret = append(ret, svc)
			continue
		}
		keep, ok := decided[feature]
		if !ok {
			d, err := featureDecision(ctx, networkID, feature)
			if err != nil {
				glog.Warningf("Keeping %s on network %s: cannot read the %s entitlement: %v", svc, networkID, feature, err)
			}
			keep = err != nil || !d.Denied()
			decided[feature] = keep
		}
		if keep {
			ret = append(ret, svc)
		} else {
			glog.V(2).Infof("Not running %s on network %s: not entitled to %s", svc, networkID, feature)
		}
	}
	return ret
}
