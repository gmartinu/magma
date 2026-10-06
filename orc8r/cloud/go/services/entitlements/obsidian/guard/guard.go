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
// Package guard enforces entitlements on the REST handlers of licensed
// features.
package guard

import (
	"fmt"
	"net/http"

	"github.com/golang/glog"
	"github.com/labstack/echo/v4"

	"magma/orc8r/cloud/go/services/entitlements"
)

// ForNetwork resolves the decision for a feature on a network; a variable
// so tests can stub the entitlements service.
var ForNetwork = entitlements.ForNetwork

// CheckEntitlement checks that the network in the request's :network_id
// may use feature:
//   - not enforcing, active or in grace: nil;
//   - frozen (expired past the grace period): nil for GET and HEAD, 403
//     for anything else;
//   - disabled: 403.
//
// When the entitlements service cannot answer, the decision is the last
// known one (entitlements.ForNetwork); with none known, every call gets
// 503, as passing reads would show a disabled tenant its data.
func CheckEntitlement(c echo.Context, feature string) error {
	networkID := c.Param("network_id")
	if networkID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "missing network ID")
	}
	read := isRead(c.Request().Method)
	d, err := ForNetwork(c.Request().Context(), networkID, feature)
	if err != nil {
		glog.Errorf("Checking the %s entitlement of network %s: %v", feature, networkID, err)
		return echo.NewHTTPError(http.StatusServiceUnavailable, fmt.Sprintf("cannot check the %s entitlement: try again", feature))
	}
	switch {
	case d.Allowed():
		return nil
	case d.Frozen() && read:
		return nil
	case d.Frozen():
		return echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("the %s entitlement of network %s has expired: read-only", feature, networkID))
	default:
		return echo.NewHTTPError(http.StatusForbidden, fmt.Sprintf("network %s is not entitled to %s", networkID, feature))
	}
}

// RequireEntitlement is CheckEntitlement as echo middleware, for
// obsidian.AttachHandlers(e, handlers, guard.RequireEntitlement(feature)).
func RequireEntitlement(feature string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			if err := CheckEntitlement(c, feature); err != nil {
				return err
			}
			return next(c)
		}
	}
}

func isRead(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}
