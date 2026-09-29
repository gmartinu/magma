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

package storage

// Device is a CPE known to the ACS.
type Device struct {
	// DeviceID is the TR-069 DeviceId triplet, OUI-ProductClass-SerialNumber.
	DeviceID string
	// NetworkID is empty until the device is claimed by a network.
	NetworkID            string
	OUI                  string
	ProductClass         string
	SerialNumber         string
	Model                string
	Firmware             string
	Handler              string
	ConnectionRequestURL string
	FirstSeenSec         int64
	LastSeenSec          int64
}

// Credentials are the per-CPE secrets for both directions of TR-069 auth.
type Credentials struct {
	DeviceID string
	// ACSUsername and ACSPasswordHash authenticate the CPE to the ACS.
	ACSUsername     string
	ACSPasswordHash string
	// ConnReqUsername and ConnReqPassword authenticate the ACS to the CPE on
	// a connection request.
	ConnReqUsername string
	ConnReqPassword string
	UpdatedSec      int64
}

// Session is server-side CWMP session state keyed by the session cookie, so
// that any replica can continue a session.
type Session struct {
	SessionID  string
	DeviceID   string
	Step       string
	CreatedSec int64
	ExpiresSec int64
}

// ACSStorage is the persistence layer of the acs service.
type ACSStorage interface {
	// Init creates the acs tables if they do not exist.
	Init() error

	// UpsertDevice creates a device or refreshes what the device reports
	// about itself.
	UpsertDevice(device *Device) error
	// GetDevice returns the device, or nil if it does not exist.
	GetDevice(deviceID string) (*Device, error)
	// ListDevices returns the devices claimed by a network, ordered by ID.
	ListDevices(networkID string) ([]*Device, error)

	// PutCredentials creates or replaces the credentials of a device.
	PutCredentials(creds *Credentials) error
	// GetCredentialsByUsername returns the credentials for an ACS username,
	// or nil if there are none.
	GetCredentialsByUsername(acsUsername string) (*Credentials, error)

	// PutSession creates or replaces a session.
	PutSession(session *Session) error
	// GetSession returns an unexpired session, or nil if there is none.
	GetSession(sessionID string) (*Session, error)
	// DeleteExpiredSessions removes all expired sessions.
	DeleteExpiredSessions() error
}
