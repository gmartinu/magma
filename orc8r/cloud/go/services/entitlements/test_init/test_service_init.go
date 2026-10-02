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
package test_init

import (
	"testing"
	"time"

	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/protos"
	servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	"magma/orc8r/cloud/go/services/entitlements/servicers/storage"
	"magma/orc8r/cloud/go/test_utils"
)

// StartTestService runs the entitlements service on an in-memory
// blobstore. Network lookups go to the tenants service (start its
// test_init too); now nil means time.Now.
func StartTestService(t *testing.T, cfg servicers.Config, now func() time.Time) {
	factory := test_utils.NewSQLBlobstore(t, "entitlements_test_service_blobstore")
	srv, lis, plis := test_utils.NewTestService(t, orc8r.ModuleName, entitlements.ServiceName)
	protos.RegisterEntitlementsServer(srv.ProtectedGrpcServer, servicers.NewServicer(storage.NewBlobstoreStore(factory), cfg, nil, now))
	go srv.RunTest(lis, plis)
}
