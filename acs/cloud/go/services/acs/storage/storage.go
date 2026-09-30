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

import "errors"

var (
	// ErrDeviceNotFound is returned for a device the ACS does not know.
	ErrDeviceNotFound = errors.New("device not found")
	// ErrDeviceClaimed is returned when claiming a device a network already
	// claimed.
	ErrDeviceClaimed = errors.New("device already claimed")
)

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
	// LastSeenSec is when the device last sent an Inform.
	LastSeenSec int64
	// InformIntervalSec is the PeriodicInformInterval the device last
	// reported, 0 while unknown.
	InformIntervalSec int64
	// LastInformEvents are the event codes of the last Inform.
	LastInformEvents []string
}

// OnlinePolicy decides whether a device is online: it is while its last
// Inform is at most IntervalMultiple periodic inform intervals old.
type OnlinePolicy struct {
	IntervalMultiple float64
	// DefaultIntervalSec stands in for the interval of devices that have not
	// reported one.
	DefaultIntervalSec int64
}

// DefaultOnlinePolicy allows one missed periodic Inform.
var DefaultOnlinePolicy = OnlinePolicy{IntervalMultiple: 2, DefaultIntervalSec: 3600}

// multipleMilli is IntervalMultiple in thousandths, so the SQL filter stays in
// integers on every dialect.
func (p OnlinePolicy) multipleMilli() int64 {
	if p.IntervalMultiple <= 0 {
		return int64(DefaultOnlinePolicy.IntervalMultiple * 1000)
	}
	return int64(p.IntervalMultiple * 1000)
}

func (p OnlinePolicy) defaultInterval() int64 {
	if p.DefaultIntervalSec <= 0 {
		return DefaultOnlinePolicy.DefaultIntervalSec
	}
	return p.DefaultIntervalSec
}

// Online reports whether the device is online at nowSec.
func (p OnlinePolicy) Online(d *Device, nowSec int64) bool {
	interval := d.InformIntervalSec
	if interval <= 0 {
		interval = p.defaultInterval()
	}
	return (nowSec-d.LastSeenSec)*1000 <= p.multipleMilli()*interval
}

// DeviceFilter selects devices. Either NetworkID or Unclaimed must be set.
type DeviceFilter struct {
	// NetworkID selects the devices claimed by the network.
	NetworkID string
	// Unclaimed selects the devices no network has claimed.
	Unclaimed bool
	// Model, when set, must equal the model name exactly.
	Model string
	// Online, when set, keeps the devices whose online state under Policy
	// at NowSec matches it.
	Online *bool
	Policy OnlinePolicy
	NowSec int64
}

// ParameterValue is the last value read of a raw TR-069 parameter.
type ParameterValue struct {
	Value      string `json:"value"`
	UpdatedSec int64  `json:"updated_sec"`
}

// Parameters are the raw parameters of a device, as last read from it.
type Parameters struct {
	DeviceID   string
	Values     map[string]ParameterValue
	UpdatedSec int64
}

// Credentials are the per-CPE secrets for both directions of TR-069 auth.
type Credentials struct {
	DeviceID string
	// ACSUsername and ACSPasswordHash authenticate the CPE to the ACS.
	ACSUsername     string
	ACSPasswordHash string
	// PendingPasswordHash is a rotated password the CPE has been sent but not
	// yet confirmed. It is accepted next to the current one until it is.
	PendingPasswordHash string
	// ConnReqUsername and ConnReqPassword authenticate the ACS to the CPE on
	// a connection request.
	ConnReqUsername string
	ConnReqPassword string
	UpdatedSec      int64
}

// Session steps.
const (
	// SessionCPERequests is the step after the InformResponse, while the CPE
	// may still send its own requests.
	SessionCPERequests = "cpe_requests"
	// SessionACSRequests is the step after the CPE's empty POST, while the
	// ACS drives the session.
	SessionACSRequests = "acs_requests"
)

// Session is server-side CWMP session state keyed by the session cookie, so
// that any replica can continue a session.
type Session struct {
	SessionID string
	DeviceID  string
	Step      string
	// Namespace is the cwmp namespace of the CPE, used for every answer.
	Namespace string
	// Root is the data model root of the CPE and Handler its handler.
	Root    string
	Handler string
	// Bootstrap is set when the CPE authenticated with the shared bootstrap
	// credentials.
	Bootstrap bool
	// RotationSent is set once the ACS sent new credentials in the session.
	RotationSent bool
	// PendingID and PendingMethod describe the ACS request awaiting an answer.
	PendingID     string
	PendingMethod string
	// PendingTaskID and TaskStep locate the task RPC being executed.
	PendingTaskID string
	TaskStep      int
	// RequestSeq numbers the ACS requests of the session.
	RequestSeq int
	CreatedSec int64
	ExpiresSec int64
}

// Task states.
const (
	TaskPending    = "pending"
	TaskInProgress = "in_progress"
	TaskDone       = "done"
	TaskFailed     = "failed"
	TaskExpired    = "expired"
)

// Task is an operation queued for a CPE and executed in its next session.
type Task struct {
	TaskID    string
	DeviceID  string
	NetworkID string
	Type      string
	// Args and Result are JSON documents whose shape depends on Type.
	Args        string
	Status      string
	Attempts    int
	MaxAttempts int
	// SessionID is the session executing the task while it is in progress,
	// or the session whose retryable fault requeued it.
	SessionID   string
	FaultCode   int
	FaultString string
	Result      string
	CreatedSec  int64
	UpdatedSec  int64
	DeadlineSec int64
}

// DeviceState is the last normalized model of a device, as JSON.
type DeviceState struct {
	DeviceID   string
	Handler    string
	Model      string
	UpdatedSec int64
}

// ReapResult counts what ReapExpired cleaned up.
type ReapResult struct {
	Sessions      int
	RequeuedTasks int
	ExpiredTasks  int
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
	// FindDevices returns the devices matching the filter, ordered by ID.
	FindDevices(filter DeviceFilter) ([]*Device, error)
	// ClaimDevice assigns an unclaimed device to a network. It returns
	// ErrDeviceNotFound or ErrDeviceClaimed when it cannot.
	ClaimDevice(deviceID, networkID string) error

	// PutCredentials creates or replaces the credentials of a device.
	PutCredentials(creds *Credentials) error
	// GetCredentials returns the credentials of a device, or nil.
	GetCredentials(deviceID string) (*Credentials, error)
	// GetCredentialsByUsername returns the credentials for an ACS username,
	// or nil if there are none.
	GetCredentialsByUsername(acsUsername string) (*Credentials, error)
	// DeleteCredentials removes the credentials of a device, which returns it
	// to the bootstrap credentials.
	DeleteCredentials(deviceID string) error

	// PutSession creates or replaces a session.
	PutSession(session *Session) error
	// GetSession returns an unexpired session, or nil if there is none.
	GetSession(sessionID string) (*Session, error)
	// GetDeviceSession returns the newest unexpired session of a device, or
	// nil.
	GetDeviceSession(deviceID string) (*Session, error)
	// EndSession removes a session and requeues the task it was executing.
	EndSession(sessionID string, reason string) error
	// EndDeviceSessions ends every session of a device, e.g. when it starts
	// a new one.
	EndDeviceSessions(deviceID string, reason string) error
	// ReapExpired ends the expired sessions, requeues their tasks and tasks
	// left in progress by a session that no longer exists, and expires the
	// pending tasks past their deadline.
	ReapExpired() (ReapResult, error)

	// CreateTask queues a task. Tasks of a device run in creation order.
	CreateTask(task *Task) error
	// GetTask returns a task, or nil.
	GetTask(taskID string) (*Task, error)
	// ListTasks returns the tasks of a device in creation order.
	ListTasks(deviceID string) ([]*Task, error)
	// ClaimNextTask locks the oldest runnable task of the device, marks it in
	// progress in the session and returns it, or nil if there is none. A task
	// requeued by a fault in the same session is not runnable in it.
	ClaimNextTask(deviceID, sessionID string) (*Task, error)
	// SaveTaskResult stores the partial result of a multi-RPC task.
	SaveTaskResult(taskID, result string) error
	// CompleteTask marks a task done with its result.
	CompleteTask(taskID, result string) error
	// FailTask records a fault. A retryable fault requeues the task while it
	// has attempts left and is before its deadline; otherwise it fails.
	FailTask(taskID string, faultCode int, faultString string, retryable bool) error

	// PutDeviceState stores the normalized model of a device.
	PutDeviceState(state *DeviceState) error
	// GetDeviceState returns the normalized model of a device, or nil.
	GetDeviceState(deviceID string) (*DeviceState, error)

	// MergeParameters records raw parameter values read from a device over
	// the ones stored, keeping the parameters it did not read.
	MergeParameters(deviceID string, values map[string]string, nowSec int64) error
	// GetParameters returns the raw parameters of a device, or nil.
	GetParameters(deviceID string) (*Parameters, error)

	// GetOrCreateSecret returns the named secret shared by every replica,
	// creating it with generate on first use.
	GetOrCreateSecret(name string, generate func() (string, error)) (string, error)
	// CountInform counts an Inform of a device in fixed windows of windowSec
	// and returns the count in the current window and when it ends.
	CountInform(deviceID string, windowSec int64) (count int, windowEndSec int64, err error)
}
