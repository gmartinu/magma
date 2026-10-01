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

package capture_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/cwmp/capture"
)

func TestLoadDir(t *testing.T) {
	sessions, err := capture.LoadDir("../testdata/captures")
	require.NoError(t, err)
	require.Len(t, sessions, 2)

	s := sessions[0]
	assert.Equal(t, "00A1B2-SIM4000-SIM0001", s.Device)
	require.Len(t, s.Exchanges, 4)
	assert.Equal(t, 1, s.Exchanges[0].Seq)
	assert.Equal(t, 401, s.Exchanges[0].Status)
	assert.False(t, s.Exchanges[0].IsCWMP())
	assert.True(t, s.Exchanges[1].IsCWMP())
	assert.Equal(t, "POST", s.Exchanges[2].Method)
	assert.Empty(t, s.Exchanges[2].Request)
	assert.Equal(t, "text/xml; charset=utf-8", s.Exchanges[3].RequestHeaders["content-type"])
	assert.Equal(t, 204, s.Exchanges[3].Status)

	probe := sessions[1]
	assert.Equal(t, "_unknown", probe.Device)
	assert.Equal(t, "GET", probe.Exchanges[0].Method)
	assert.False(t, probe.Exchanges[0].IsCWMP())
}

func TestLoadDirMissingAndStray(t *testing.T) {
	sessions, err := capture.LoadDir(filepath.Join(t.TempDir(), "none"))
	assert.NoError(t, err)
	assert.Empty(t, sessions)

	root := t.TempDir()
	dir := filepath.Join(root, "dev", "s1")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.jsonl"), []byte("{}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "001-request.raw"), []byte{0x1f, 0x8b}, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "001-request.xml"), []byte("<x/>"), 0o644))
	sessions, err = capture.LoadDir(root)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, []byte("<x/>"), sessions[0].Exchanges[0].Request)
	// No headers captured: treated as a POST with unknown status.
	assert.True(t, sessions[0].Exchanges[0].IsCWMP())

	require.NoError(t, os.WriteFile(filepath.Join(dir, "001-request.headers.json"), []byte("{"), 0o644))
	_, err = capture.LoadDir(root)
	assert.Error(t, err)
}
