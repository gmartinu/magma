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

package cwmp_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp"
	"magma/acs/cloud/go/services/acs/cwmp/capture"
)

// CapturesDir holds sessions recorded by the lab proxy
// (tools/acs-lab/proxy). Copying a captures/<device>/<session>/ tree from the
// lab here is enough for it to be replayed.
const CapturesDir = "testdata/captures"

func TestReplayCaptures(t *testing.T) {
	sessions, err := capture.LoadDir(CapturesDir)
	require.NoError(t, err)
	require.NotEmpty(t, sessions)

	for _, s := range sessions {
		t.Run(s.Device+"/"+s.ID, func(t *testing.T) {
			lastACSRequestID := ""
			for _, ex := range s.Exchanges {
				if !ex.IsCWMP() {
					continue
				}
				if req := decodeCaptured(t, ex, "request", ex.Request); req != nil {
					if inform, ok := req.Body.(*cwmp.Inform); ok && s.Device != "_unknown" {
						assert.Equal(t, s.Device, inform.DeviceId.DeviceID())
					}
					if lastACSRequestID != "" && req.ID != lastACSRequestID {
						t.Logf("%03d: CPE answered ID %q to ACS request %q", ex.Seq, req.ID, lastACSRequestID)
					}
				}
				lastACSRequestID = ""
				if ex.Status != 200 {
					continue
				}
				if resp := decodeCaptured(t, ex, "response", ex.Response); resp != nil && isACSRequest(resp.Body) {
					lastACSRequestID = resp.ID
				}
			}
		})
	}
}

// decodeCaptured decodes a captured body and checks that re-encoding it
// yields the same message. It returns nil for an empty body.
func decodeCaptured(t *testing.T, ex capture.Exchange, what string, body []byte) *cwmp.Envelope {
	if cwmp.IsEmpty(body) {
		return nil
	}
	where := fmt.Sprintf("%03d-%s", ex.Seq, what)
	env, err := cwmp.Decode(body)
	require.NoError(t, err, where)
	if _, unknown := env.Body.(*cwmp.Unknown); unknown {
		t.Logf("%s: no codec type for %s", where, env.Body.Method())
		return env
	}
	out, err := cwmp.Encode(env)
	require.NoError(t, err, where)
	again, err := cwmp.Decode(out)
	require.NoError(t, err, where)
	assert.Equal(t, env, again, where)
	return env
}

func isACSRequest(m cwmp.Message) bool {
	switch m.(type) {
	case *cwmp.InformResponse, *cwmp.TransferCompleteResponse, *cwmp.GetRPCMethodsResponse, *cwmp.Fault:
		return false
	}
	return !strings.HasSuffix(m.Method(), "Response")
}
