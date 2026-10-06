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

package cpestate_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/serde"
)

// The JSON acsd's CpeView (cpe_state.py) serializes to.
const reported = `{"cpe_key": "IMSI001010000000001", "mode": "core", "imsi": "IMSI001010000000001",
	"serial_number": "SN1", "oui": "00259E", "product_class": "Titan4000", "software_version": "1.2.3",
	"handler": "titan", "last_inform": 1700000000.25, "informs_total": 3, "online": true,
	"pending_tasks": 2, "last_session": {"cpe_key": "IMSI001010000000001", "session_id": "s1",
	"result": "completed", "reason": "", "started": 1.0, "ended": 2.0, "tasks_done": 1,
	"tasks_failed": 0, "faults": 0, "mode": "core"},
	"model": {"identity": {"model_name": "Titan 4000"}, "cellular": {"rsrp": -95}},
	"reach": "next_inform", "reach_reason": "behind_nat", "next_inform": 1700000300.25}`

func TestDeserializeAcsdView(t *testing.T) {
	v, err := serde.Deserialize([]byte(reported), lte.CPEAcsStateType, cpestate.Serdes)
	require.NoError(t, err)
	view := v.(*cpestate.CpeView)
	assert.Equal(t, "IMSI001010000000001", view.CpeKey)
	assert.Equal(t, cpestate.ModeCore, view.Mode)
	assert.Equal(t, 1700000000.25, view.LastInform)
	assert.Equal(t, int32(2), view.PendingTasks)
	assert.Equal(t, int32(1), view.LastSession.TasksDone)
	assert.Equal(t, "Titan 4000", view.ModelName())
	assert.Equal(t, cpestate.ReachNextInform, view.Reach)
	assert.Equal(t, "behind_nat", view.ReachReason)
	assert.Equal(t, 1700000300.25, view.NextInform)
	assert.NoError(t, view.ValidateModel(context.Background()))
}

func TestValidateRequiresCpeKey(t *testing.T) {
	assert.Error(t, (&cpestate.CpeView{}).ValidateModel(context.Background()))
	assert.Equal(t, "", (&cpestate.CpeView{CpeKey: "CLAIM1"}).ModelName())
}
