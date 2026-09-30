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

package storage_test

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/storage"
)

func TestSealer(t *testing.T) {
	_, err := storage.NewSealer([]byte("short"))
	assert.Error(t, err)

	s, err := storage.NewSealer(testKey())
	require.NoError(t, err)
	a, err := s.Seal("secret", "dev1")
	require.NoError(t, err)
	b, err := s.Seal("secret", "dev1")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "every seal uses a fresh nonce")

	got, err := s.Open(a, "dev1")
	require.NoError(t, err)
	assert.Equal(t, "secret", got)

	_, err = s.Open(a, "dev2")
	assert.Error(t, err, "bound to its row")
	_, err = s.Open("secret", "dev1")
	assert.Error(t, err, "plaintext is not accepted")
	_, err = s.Open("v1:AAAA", "dev1")
	assert.Error(t, err)

	other := testKey()
	other[0] ^= 1
	o, err := storage.NewSealer(other)
	require.NoError(t, err)
	_, err = o.Open(a, "dev1")
	assert.Error(t, err, "wrong key")
}

func TestParseEncryptionKey(t *testing.T) {
	key, err := storage.ParseEncryptionKey(base64.StdEncoding.EncodeToString(testKey()) + "\n")
	require.NoError(t, err)
	assert.Equal(t, testKey(), key)
	_, err = storage.ParseEncryptionKey("not base64!")
	assert.Error(t, err)
	_, err = storage.ParseEncryptionKey(base64.StdEncoding.EncodeToString([]byte("16 bytes only..!")))
	assert.Error(t, err)
}
