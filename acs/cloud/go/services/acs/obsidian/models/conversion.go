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

package models

import (
	"time"

	"github.com/go-openapi/strfmt"

	"magma/acs/cloud/go/services/acs/storage"
)

func (m *AcsDevice) FromStorage(d *storage.Device) *AcsDevice {
	m.DeviceID = d.DeviceID
	m.Oui = d.OUI
	m.ProductClass = d.ProductClass
	m.SerialNumber = d.SerialNumber
	m.Model = d.Model
	m.Firmware = d.Firmware
	m.Handler = d.Handler
	m.FirstSeen = strfmt.DateTime(time.Unix(d.FirstSeenSec, 0).UTC())
	m.LastSeen = strfmt.DateTime(time.Unix(d.LastSeenSec, 0).UTC())
	return m
}
