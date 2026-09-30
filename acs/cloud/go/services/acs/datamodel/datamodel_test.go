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

package datamodel_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"magma/acs/cloud/go/services/acs/datamodel"
)

func f(v float64) *float64 { return &v }
func i(v int64) *int64     { return &v }
func b(v bool) *bool       { return &v }

func TestMerge(t *testing.T) {
	m := &datamodel.Model{
		Root:     datamodel.RootTR181,
		Identity: datamodel.Identity{SerialNumber: "S", ModelName: "M"},
		Cellular: datamodel.Cellular{RSRP: f(-100), Band: "n41"},
	}
	m.Merge(&datamodel.Model{Identity: datamodel.Identity{ModelName: "M2"}, Cellular: datamodel.Cellular{RSRP: f(-90)}})
	m.Merge(nil)
	assert.Equal(t, &datamodel.Model{
		Root:     datamodel.RootTR181,
		Identity: datamodel.Identity{SerialNumber: "S", ModelName: "M2"},
		Cellular: datamodel.Cellular{RSRP: f(-90), Band: "n41"},
	}, m)
}
