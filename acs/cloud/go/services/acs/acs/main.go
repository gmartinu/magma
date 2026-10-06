/*
Copyright 2026 The Magma Authors.

This source code is licensed under the BSD-style license found in the
LICENSE file in the root directory of this source tree.

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"github.com/golang/glog"

	"magma/acs/cloud/go/acs"
	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/reports"
	"magma/acs/cloud/go/services/acs/sessionlog"
	"magma/orc8r/cloud/go/blobstore"
	"magma/orc8r/cloud/go/service"
	"magma/orc8r/cloud/go/services/eventd/eventd_client"
	"magma/orc8r/cloud/go/services/obsidian"
	swagger_protos "magma/orc8r/cloud/go/services/obsidian/swagger/protos"
	swagger_servicers "magma/orc8r/cloud/go/services/obsidian/swagger/servicers/protected"
	state_protos "magma/orc8r/cloud/go/services/state/protos"
	"magma/orc8r/cloud/go/sqorc"
	"magma/orc8r/cloud/go/storage"
)

func main() {
	srv, err := service.NewOrchestratorService(acs.ModuleName, acs_service.ServiceName)
	if err != nil {
		glog.Fatalf("Error creating %s service: %s", acs_service.ServiceName, err)
	}

	db, err := sqorc.Open(storage.GetSQLDriver(), storage.GetDatabaseSource())
	if err != nil {
		glog.Fatalf("Error opening db connection: %s", err)
	}
	factory := blobstore.NewSQLStoreFactory(reports.TableName, db, sqorc.GetSqlBuilder())
	if err := factory.InitializeFactory(); err != nil {
		glog.Fatalf("Error initializing the CPE report table: %s", err)
	}
	cpeReports := reports.NewStore(factory)
	state_protos.RegisterIndexerServer(srv.ProtectedGrpcServer, reports.NewIndexerServicer(cpeReports))

	h := handlers.NewHandlers(handlers.NewSyncRPCCpeManagers())
	// The same Elasticsearch, from orc8r's elastic.yml, as the events API.
	if es, err := eventd_client.GetElasticClient(); err != nil {
		glog.Errorf("Session log search disabled: %s", err)
	} else {
		h.WithLogs(sessionlog.NewElastic(es))
	}
	obsidian.AttachHandlers(srv.EchoServer, h.GetHandlers())
	swagger_protos.RegisterSwaggerSpecServer(srv.ProtectedGrpcServer, swagger_servicers.NewSpecServicerFromFile(acs_service.ServiceName))

	err = srv.Run()
	if err != nil {
		glog.Fatalf("Error while running %s service and echo server: %s", acs_service.ServiceName, err)
	}
}
