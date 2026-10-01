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

// Package acs is the TR-069 Auto Configuration Server for fixed wireless
// access CPEs.
package acs

import (
	"fmt"
	"os"
	"time"

	"magma/acs/cloud/go/services/acs/auth"
	"magma/acs/cloud/go/services/acs/cwmp/server"
	"magma/acs/cloud/go/services/acs/storage"
)

const ServiceName = "acs"

// Environment variables that override the bootstrap credentials of the
// config file, so they can come from a Kubernetes secret.
const (
	BootstrapUsernameEnv = "ACS_BOOTSTRAP_USERNAME"
	BootstrapPasswordEnv = "ACS_BOOTSTRAP_PASSWORD"
	// EncryptionKeyEnv holds the base64 32 byte key that seals the
	// ConnectionRequest passwords at rest. It is env only, never in the file.
	EncryptionKeyEnv = "ACS_ENCRYPTION_KEY"
)

type Config struct {
	CwmpPort int `yaml:"cwmp_port"`
	// CwmpRealm is the Digest realm; stored password hashes are bound to it.
	CwmpRealm string `yaml:"cwmp_realm"`
	// BasicAuth is never, tls_only (default) or always.
	BasicAuth string `yaml:"basic_auth"`
	// TrustProxyHeaders takes TLS and the client address from the
	// X-Forwarded-Proto and X-Real-IP headers of the ingress proxy.
	TrustProxyHeaders bool   `yaml:"trust_proxy_headers"`
	BootstrapUsername string `yaml:"bootstrap_username"`
	BootstrapPassword string `yaml:"bootstrap_password"`
	// RotateCredentials defaults to true.
	RotateCredentials      *bool `yaml:"rotate_credentials"`
	SessionTimeoutSec      int   `yaml:"session_timeout_sec"`
	NonceTTLSec            int   `yaml:"nonce_ttl_sec"`
	InformRateLimit        int   `yaml:"inform_rate_limit"`
	InformRateWindowSec    int   `yaml:"inform_rate_window_sec"`
	MaintenanceIntervalSec int   `yaml:"maintenance_interval_sec"`
	TaskMaxAttempts        int   `yaml:"task_max_attempts"`
	TaskTTLSec             int   `yaml:"task_ttl_sec"`
	// A device is online while its last Inform is at most
	// OnlineIntervalMultiple periodic inform intervals old, using
	// OnlineDefaultIntervalSec for devices that have not reported one.
	OnlineIntervalMultiple   float64 `yaml:"online_interval_multiple"`
	OnlineDefaultIntervalSec int64   `yaml:"online_default_interval_sec"`
}

// ServerConfig returns the runtime config of the CWMP endpoint.
func (c Config) ServerConfig() (server.Config, error) {
	policy, err := auth.ParseBasicPolicy(c.BasicAuth)
	if err != nil {
		return server.Config{}, err
	}
	rotate := c.RotateCredentials == nil || *c.RotateCredentials
	cfg := server.Config{
		Realm:             c.CwmpRealm,
		BasicPolicy:       policy,
		TrustProxyHeaders: c.TrustProxyHeaders,
		BootstrapUsername: c.BootstrapUsername,
		BootstrapPassword: c.BootstrapPassword,
		RotateCredentials: rotate,
		SessionTimeout:    seconds(c.SessionTimeoutSec),
		NonceTTL:          seconds(c.NonceTTLSec),
		InformRateLimit:   c.InformRateLimit,
		InformRateWindow:  seconds(c.InformRateWindowSec),
	}
	if v := os.Getenv(BootstrapUsernameEnv); v != "" {
		cfg.BootstrapUsername = v
	}
	if v := os.Getenv(BootstrapPasswordEnv); v != "" {
		cfg.BootstrapPassword = v
	}
	return cfg, nil
}

// Sealer returns the sealer of the key in EncryptionKeyEnv. Without the key
// it returns nil, which is an error when credentials are rotated: rotation
// stores a ConnectionRequest password and the storage refuses it unsealed.
func (c Config) Sealer() (*storage.Sealer, error) {
	encoded := os.Getenv(EncryptionKeyEnv)
	if encoded == "" {
		if c.RotateCredentials == nil || *c.RotateCredentials {
			return nil, fmt.Errorf("%s is required while rotate_credentials is on", EncryptionKeyEnv)
		}
		return nil, nil
	}
	key, err := storage.ParseEncryptionKey(encoded)
	if err != nil {
		return nil, err
	}
	return storage.NewSealer(key)
}

// OnlinePolicy is how the REST API derives the online state of a device.
func (c Config) OnlinePolicy() storage.OnlinePolicy {
	p := storage.DefaultOnlinePolicy
	if c.OnlineIntervalMultiple > 0 {
		p.IntervalMultiple = c.OnlineIntervalMultiple
	}
	if c.OnlineDefaultIntervalSec > 0 {
		p.DefaultIntervalSec = c.OnlineDefaultIntervalSec
	}
	return p
}

// MaintenanceInterval is how often expired sessions and tasks are reaped.
func (c Config) MaintenanceInterval() time.Duration {
	if c.MaintenanceIntervalSec <= 0 {
		return 30 * time.Second
	}
	return seconds(c.MaintenanceIntervalSec)
}

// TaskDefaults are the attempts and time to live of a task created without
// them.
func (c Config) TaskDefaults() (maxAttempts int, ttl time.Duration) {
	maxAttempts, ttl = c.TaskMaxAttempts, seconds(c.TaskTTLSec)
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	return maxAttempts, ttl
}

func seconds(n int) time.Duration {
	return time.Duration(n) * time.Second
}
