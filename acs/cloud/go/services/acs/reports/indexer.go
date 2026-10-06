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

package reports

import (
	"context"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/services/state/indexer"
	"magma/orc8r/cloud/go/services/state/protos"
	state_types "magma/orc8r/cloud/go/services/state/types"
)

const (
	// IndexerVersion of the acs state indexer (service_registry.yml).
	IndexerVersion indexer.Version = 1
)

// IndexerTypes are the state types the acs service indexes.
var IndexerTypes = []string{lte.CPEAcsStateType}

type indexerServicer struct {
	store *Store
}

// NewIndexerServicer indexes the cpe_acs state into store.
func NewIndexerServicer(store *Store) protos.IndexerServer {
	return &indexerServicer{store: store}
}

func (i *indexerServicer) Index(_ context.Context, req *protos.IndexRequest) (*protos.IndexResponse, error) {
	states, err := state_types.MakeStatesByID(req.States, cpestate.Serdes)
	if err != nil {
		return nil, err
	}
	stErrs := state_types.StateErrors{}
	var reports []Report
	for id, st := range states {
		view, ok := st.ReportedState.(*cpestate.CpeView)
		if !ok || st.ReporterID == "" {
			stErrs[id] = fmt.Errorf("cpe_acs state %s has no view or reporter", id.DeviceID)
			continue
		}
		// The state key is the cpe_key; the view's own wins only if the
		// gateway sends it, which acsd always does.
		if view.CpeKey == "" {
			view.CpeKey = id.DeviceID
		}
		reports = append(reports, Report{HardwareID: st.ReporterID, TimeMs: st.TimeMs, View: view})
	}
	if err := i.store.Put(req.NetworkId, reports); err != nil {
		return nil, status.Errorf(codes.Internal, "indexing cpe_acs reports: %v", err)
	}
	return &protos.IndexResponse{StateErrors: state_types.MakeProtoStateErrors(stErrs)}, nil
}

func (i *indexerServicer) DeIndex(_ context.Context, req *protos.DeIndexRequest) (*protos.DeIndexResponse, error) {
	states, err := state_types.MakeSerializedStatesByID(req.States)
	if err != nil {
		return nil, err
	}
	for id, st := range states {
		if err := i.store.Delete(req.NetworkId, id.DeviceID, st.ReporterID); err != nil {
			return nil, status.Errorf(codes.Internal, "removing cpe_acs report: %v", err)
		}
	}
	return &protos.DeIndexResponse{}, nil
}

func (i *indexerServicer) PrepareReindex(context.Context, *protos.PrepareReindexRequest) (*protos.PrepareReindexResponse, error) {
	return &protos.PrepareReindexResponse{}, nil
}

func (i *indexerServicer) CompleteReindex(_ context.Context, req *protos.CompleteReindexRequest) (*protos.CompleteReindexResponse, error) {
	if req.FromVersion == 0 && req.ToVersion == uint32(IndexerVersion) {
		return &protos.CompleteReindexResponse{}, nil
	}
	return nil, status.Errorf(codes.InvalidArgument, "unsupported CompleteReindex from %d to %d", req.FromVersion, req.ToVersion)
}
