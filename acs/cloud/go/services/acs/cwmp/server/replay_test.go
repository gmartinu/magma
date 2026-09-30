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

package server_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/auth"
	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/capture"
)

// TestReplayCapturedInforms posts every Inform captured in the lab to the
// ACS and checks it opens a session answered in the CPE's namespace.
func TestReplayCapturedInforms(t *testing.T) {
	cfg := defaultConfig()
	cfg.BasicPolicy = auth.BasicAlways
	h := newHarness(t, cfg, 1)
	basic := map[string]string{"Authorization": auth.BasicAuthorization(bootstrapUser, bootstrapPass)}

	sessions, err := capture.LoadDir("../testdata/captures")
	require.NoError(t, err)
	informs := 0
	for _, s := range sessions {
		for _, ex := range s.Exchanges {
			if !ex.IsCWMP() || cwmp.IsEmpty(ex.Request) {
				continue
			}
			in, err := cwmp.Decode(ex.Request)
			require.NoError(t, err)
			inform, ok := in.Body.(*cwmp.Inform)
			if !ok {
				continue
			}
			informs++
			resp := rawPost(t, h, ex.Request, basic)
			require.Equal(t, http.StatusOK, resp.StatusCode, "%s/%s %03d", s.Device, s.ID, ex.Seq)
			out := decodeResp(t, resp)
			assert.Equal(t, in.ID, out.ID)
			assert.Equal(t, in.Namespace, out.Namespace)
			assert.IsType(t, &cwmp.InformResponse{}, out.Body)

			d, err := h.store.GetDevice(inform.DeviceId.DeviceID())
			require.NoError(t, err)
			require.NotNil(t, d)
			assert.NotEmpty(t, d.Handler)
		}
	}
	assert.NotZero(t, informs)
}
