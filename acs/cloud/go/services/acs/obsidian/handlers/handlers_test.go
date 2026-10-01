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

package handlers_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"magma/acs/cloud/go/services/acs/obsidian/handlers"
	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/storage"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/obsidian/tests"
	"magma/orc8r/cloud/go/sqorc"
)

func TestListDevices(t *testing.T) {
	db, err := sqorc.Open("sqlite3", ":memory:?_foreign_keys=1")
	require.NoError(t, err)
	store := storage.NewSQLACSStorage(db, sqorc.GetSqlBuilder())
	require.NoError(t, store.Init())

	e := echo.New()
	listDevices := tests.GetHandlerByPathAndMethod(t, handlers.NewHandlers(store).GetHandlers(), handlers.DevicesPath, obsidian.GET).HandlerFunc

	tc := tests.Test{
		Method:         "GET",
		URL:            "/magma/v1/acs/n1/devices",
		Handler:        listDevices,
		ParamNames:     []string{"network_id"},
		ParamValues:    []string{"n1"},
		ExpectedStatus: http.StatusOK,
		ExpectedResult: tests.JSONMarshaler([]*models.AcsDevice{}),
	}
	tests.RunUnitTest(t, e, tc)

	require.NoError(t, store.UpsertDevice(&storage.Device{
		DeviceID:     "00259E-Titan4000-SN1",
		NetworkID:    "n1",
		OUI:          "00259E",
		ProductClass: "Titan4000",
		SerialNumber: "SN1",
		Firmware:     "1.0.0",
		FirstSeenSec: 100,
		LastSeenSec:  200,
	}))
	require.NoError(t, store.UpsertDevice(&storage.Device{
		DeviceID:     "00259E-Titan4000-SN2",
		NetworkID:    "n2",
		OUI:          "00259E",
		SerialNumber: "SN2",
		FirstSeenSec: 100,
		LastSeenSec:  100,
	}))
	tc.ExpectedResult = tests.JSONMarshaler([]*models.AcsDevice{{
		DeviceID:     "00259E-Titan4000-SN1",
		Oui:          "00259E",
		ProductClass: "Titan4000",
		SerialNumber: "SN1",
		Firmware:     "1.0.0",
		FirstSeen:    strfmt.DateTime(time.Unix(100, 0).UTC()),
		LastSeen:     strfmt.DateTime(time.Unix(200, 0).UTC()),
	}})
	tests.RunUnitTest(t, e, tc)

	tc.ParamNames, tc.ParamValues = nil, nil
	tc.ExpectedStatus = http.StatusBadRequest
	tc.ExpectedResult = nil
	tc.ExpectedError = "Missing Network ID"
	tests.RunUnitTest(t, e, tc)

	tc = tests.Test{
		Method:         "GET",
		URL:            "/magma/v1/acs/n1/devices",
		Handler:        handlers.NewHandlers(failingStore{store}).GetHandlers()[0].HandlerFunc,
		ParamNames:     []string{"network_id"},
		ParamValues:    []string{"n1"},
		ExpectedStatus: http.StatusInternalServerError,
		ExpectedError:  "db down",
	}
	tests.RunUnitTest(t, e, tc)
}

type failingStore struct {
	storage.ACSStorage
}

func (failingStore) ListDevices(string) ([]*storage.Device, error) {
	return nil, errors.New("db down")
}
