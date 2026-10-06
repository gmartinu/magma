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

	"magma/orc8r/cloud/go/services/entitlements/protos"
)

// StubFetch replaces the entitlements service lookup until the returned
// func runs.
func StubFetch(f func(ctx context.Context, networkID string) (*protos.NetworkEntitlements, error)) func() {
	saved := fetchNetworkEntitlements
	fetchNetworkEntitlements = f
	return func() { fetchNetworkEntitlements = saved }
}
