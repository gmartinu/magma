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
package main

import (
	"flag"
	"strconv"

	"github.com/golang/glog"

	"magma/orc8r/cloud/go/blobstore"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/service"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/obsidian/handlers"
	"magma/orc8r/cloud/go/services/entitlements/protos"
	servicers "magma/orc8r/cloud/go/services/entitlements/servicers/protected"
	"magma/orc8r/cloud/go/services/entitlements/servicers/storage"
	"magma/orc8r/cloud/go/services/obsidian"
	swagger_protos "magma/orc8r/cloud/go/services/obsidian/swagger/protos"
	swagger_servicers "magma/orc8r/cloud/go/services/obsidian/swagger/servicers/protected"
	"magma/orc8r/cloud/go/sqorc"
	storage2 "magma/orc8r/cloud/go/storage"
	"magma/orc8r/lib/go/service/config"
)

// The Helm value entitlements.enforce reaches the service as this flag.
var enforce = flag.String("enforce", "", "true or false; overrides enforce in entitlements.yml")

func main() {
	srv, err := service.NewOrchestratorService(orc8r.ModuleName, entitlements.ServiceName)
	if err != nil {
		glog.Fatalf("Error creating entitlements service: %v", err)
	}
	cfg := servicers.Config{}
	if _, _, err := config.GetStructuredServiceConfig(orc8r.ModuleName, entitlements.ServiceName, &cfg); err != nil {
		glog.Warningf("No entitlements.yml (%v); entitlements are not enforced", err)
	}
	if *enforce != "" {
		cfg.Enforce, err = strconv.ParseBool(*enforce)
		if err != nil {
			glog.Fatalf("Invalid -enforce %q: %v", *enforce, err)
		}
	}
	glog.Infof("Entitlements enforce=%t allowUntenanted=%t", cfg.Enforce, cfg.AllowUntenanted)

	db, err := sqorc.Open(storage2.GetSQLDriver(), storage2.GetDatabaseSource())
	if err != nil {
		glog.Fatalf("Failed to connect to database: %v", err)
	}
	factory := blobstore.NewSQLStoreFactory(entitlements.DBTableName, db, sqorc.GetSqlBuilder())
	if err := factory.InitializeFactory(); err != nil {
		glog.Fatalf("Error initializing entitlements database: %v", err)
	}
	protos.RegisterEntitlementsServer(srv.ProtectedGrpcServer, servicers.NewServicer(storage.NewBlobstoreStore(factory), cfg, nil, nil))
	swagger_protos.RegisterSwaggerSpecServer(srv.ProtectedGrpcServer, swagger_servicers.NewSpecServicerFromFile(entitlements.ServiceName))
	obsidian.AttachHandlers(srv.EchoServer, handlers.GetObsidianHandlers())

	if err := srv.Run(); err != nil {
		glog.Fatalf("Error running service: %v", err)
	}
}
