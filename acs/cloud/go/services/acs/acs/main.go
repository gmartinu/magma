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
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/datamodel"
	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/sessionlog"
	acs_storage "magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/service"
	"magma/orc8r/cloud/go/services/eventd/eventd_client"
	"magma/orc8r/cloud/go/services/obsidian"
	swagger_protos "magma/orc8r/cloud/go/services/obsidian/swagger/protos"
	swagger_servicers "magma/orc8r/cloud/go/services/obsidian/swagger/servicers/protected"
	"magma/orc8r/cloud/go/sqorc"
	"magma/orc8r/cloud/go/storage"
	"magma/orc8r/lib/go/service/config"
)

func main() {
	srv, err := service.NewOrchestratorService(acs.ModuleName, acs_service.ServiceName)
	if err != nil {
		glog.Fatalf("Error creating %s service: %s", acs_service.ServiceName, err)
	}

	var serviceConfig acs_service.Config
	config.MustGetStructuredServiceConfig(acs.ModuleName, acs_service.ServiceName, &serviceConfig)

	db, err := sqorc.Open(storage.GetSQLDriver(), storage.GetDatabaseSource())
	if err != nil {
		glog.Fatalf("Error opening db connection: %s", err)
	}
	sealer, err := serviceConfig.Sealer()
	if err != nil {
		glog.Fatalf("Error in %s config: %s", acs_service.ServiceName, err)
	}
	store := acs_storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder(), acs_storage.WithSealer(sealer))
	if err := store.Init(); err != nil {
		glog.Fatalf("Error initializing %s storage: %s", acs_service.ServiceName, err)
	}

	cwmpConfig, err := serviceConfig.ServerConfig()
	if err != nil {
		glog.Fatalf("Error in %s config: %s", acs_service.ServiceName, err)
	}
	kpis := serviceConfig.KPIReporter()
	cwmpServer, err := server.New(cwmpConfig, store, datamodel.NewRegistry(),
		server.WithKPIReporter(kpis), server.WithSessionLog(serviceConfig.SessionLogSink()))
	if err != nil {
		glog.Fatalf("Error creating CWMP server: %s", err)
	}
	go func() {
		err := cwmpServer.HTTPServer(serviceConfig.CwmpPort).ListenAndServe()
		glog.Fatalf("CWMP listener on port %d stopped: %s", serviceConfig.CwmpPort, err)
	}()
	go cwmpServer.RunMaintenance(make(chan struct{}), serviceConfig.MaintenanceInterval())
	if kpis != nil {
		go kpis.RunOnlineSweep(make(chan struct{}), store, serviceConfig.OnlinePolicy(), serviceConfig.OnlineMetricsInterval())
	}

	restHandlers := handlers.NewHandlers(store)
	restHandlers.TaskMaxAttempts, restHandlers.TaskTTL = serviceConfig.TaskDefaults()
	restHandlers.Online = serviceConfig.OnlinePolicy()
	restHandlers.Logs = sessionlog.NewElasticSearcher(eventd_client.GetElasticClient)
	obsidian.AttachHandlers(srv.EchoServer, restHandlers.GetHandlers())
	swagger_protos.RegisterSwaggerSpecServer(srv.ProtectedGrpcServer, swagger_servicers.NewSpecServicerFromFile(acs_service.ServiceName))

	err = srv.Run()
	if err != nil {
		glog.Fatalf("Error while running %s service and echo server: %s", acs_service.ServiceName, err)
	}
}
