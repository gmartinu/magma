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

package acs_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/auth"
	"magma/acs/cloud/go/services/acs/storage"
)

func TestServerConfig(t *testing.T) {
	t.Setenv(acs_service.BootstrapUsernameEnv, "")
	t.Setenv(acs_service.BootstrapPasswordEnv, "")
	cfg, err := acs_service.Config{CwmpPort: 7547}.ServerConfig()
	require.NoError(t, err)
	assert.Equal(t, auth.BasicTLSOnly, cfg.BasicPolicy)
	assert.True(t, cfg.RotateCredentials)
	assert.False(t, cfg.TrustProxyHeaders)
	assert.Equal(t, 30*time.Second, acs_service.Config{}.MaintenanceInterval())
	n, ttl := acs_service.Config{}.TaskDefaults()
	assert.Equal(t, 3, n)
	assert.Equal(t, 7*24*time.Hour, ttl)

	off := false
	c := acs_service.Config{
		BasicAuth: "never", TrustProxyHeaders: true, RotateCredentials: &off,
		BootstrapUsername: "file-user", BootstrapPassword: "file-pass",
		SessionTimeoutSec: 30, NonceTTLSec: 60, InformRateLimit: 5, InformRateWindowSec: 120,
		MaintenanceIntervalSec: 10, TaskMaxAttempts: 1, TaskTTLSec: 60,
	}
	t.Setenv(acs_service.BootstrapPasswordEnv, "secret-pass")
	cfg, err = c.ServerConfig()
	require.NoError(t, err)
	assert.Equal(t, auth.BasicNever, cfg.BasicPolicy)
	assert.False(t, cfg.RotateCredentials)
	assert.True(t, cfg.TrustProxyHeaders)
	assert.Equal(t, "file-user", cfg.BootstrapUsername)
	assert.Equal(t, "secret-pass", cfg.BootstrapPassword)
	assert.Equal(t, 30*time.Second, cfg.SessionTimeout)
	assert.Equal(t, time.Minute, cfg.NonceTTL)
	assert.Equal(t, 5, cfg.InformRateLimit)
	assert.Equal(t, 2*time.Minute, cfg.InformRateWindow)
	assert.Equal(t, 10*time.Second, c.MaintenanceInterval())
	n, ttl = c.TaskDefaults()
	assert.Equal(t, 1, n)
	assert.Equal(t, time.Minute, ttl)

	_, err = acs_service.Config{BasicAuth: "sometimes"}.ServerConfig()
	assert.Error(t, err)
}

func TestOnlinePolicy(t *testing.T) {
	assert.Equal(t, storage.DefaultOnlinePolicy, acs_service.Config{}.OnlinePolicy())
	p := acs_service.Config{OnlineIntervalMultiple: 1.5, OnlineDefaultIntervalSec: 600}.OnlinePolicy()
	assert.Equal(t, storage.OnlinePolicy{IntervalMultiple: 1.5, DefaultIntervalSec: 600}, p)
}

func TestSealer(t *testing.T) {
	off := false
	t.Setenv(acs_service.EncryptionKeyEnv, "")
	_, err := acs_service.Config{}.Sealer()
	assert.Error(t, err, "rotation stores a ConnectionRequest password, so the key is required")
	s, err := acs_service.Config{RotateCredentials: &off}.Sealer()
	assert.NoError(t, err)
	assert.Nil(t, s)

	t.Setenv(acs_service.EncryptionKeyEnv, "dG9vIHNob3J0")
	_, err = acs_service.Config{}.Sealer()
	assert.Error(t, err)

	t.Setenv(acs_service.EncryptionKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	s, err = acs_service.Config{}.Sealer()
	require.NoError(t, err)
	assert.NotNil(t, s)
}
