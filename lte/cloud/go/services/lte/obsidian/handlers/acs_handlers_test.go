/*
 * Copyright 2026 The Magma Authors.
 *
 * This source code is licensed under the BSD-style license found in the
 * LICENSE file in the root directory of this source tree.
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */
package handlers_test

import (
	"context"
	"testing"

	"github.com/go-openapi/swag"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"

	"magma/lte/cloud/go/lte"
	"magma/lte/cloud/go/serdes"
	"magma/lte/cloud/go/services/lte/obsidian/handlers"
	lteModels "magma/lte/cloud/go/services/lte/obsidian/models"
	"magma/orc8r/cloud/go/services/configurator"
	configuratorTestInit "magma/orc8r/cloud/go/services/configurator/test_init"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/obsidian/tests"
	"magma/orc8r/lib/go/merrors"
)

func TestNetworkAcsConfigHandlers(t *testing.T) {
	configuratorTestInit.StartTestService(t)
	seedNetworks(t)
	e := echo.New()
	path := "/magma/v1/lte/:network_id/acs"
	url := "/magma/v1/lte/n1/acs"
	hs := handlers.GetHandlers()
	get := tests.GetHandlerByPathAndMethod(t, hs, path, obsidian.GET).HandlerFunc
	put := tests.GetHandlerByPathAndMethod(t, hs, path, obsidian.PUT).HandlerFunc
	del := tests.GetHandlerByPathAndMethod(t, hs, path, obsidian.DELETE).HandlerFunc
	params := func(tc tests.Test) tests.Test {
		tc.URL, tc.ParamNames, tc.ParamValues = url, []string{"network_id"}, []string{"n1"}
		return tc
	}

	// Unset: an empty config, acsd runs on its defaults.
	tests.RunUnitTest(t, e, params(tests.Test{
		Method: "GET", Handler: get, ExpectedStatus: 200,
		ExpectedResult: tests.JSONMarshaler(&lteModels.NetworkAcsConfigs{}),
	}))

	cfg := &lteModels.NetworkAcsConfigs{
		Mode:                   swag.String(lteModels.AcsModeFrozen),
		PeriodicInformInterval: swag.Int32(300),
		Port:                   swag.Int32(7547),
	}
	tests.RunUnitTest(t, e, params(tests.Test{Method: "PUT", Handler: put, Payload: cfg, ExpectedStatus: 204}))
	stored, err := configurator.LoadNetworkConfig(context.Background(), "n1", lte.AcsNetworkConfigType, serdes.Network)
	assert.NoError(t, err)
	assert.Equal(t, cfg, stored)
	tests.RunUnitTest(t, e, params(tests.Test{
		Method: "GET", Handler: get, ExpectedStatus: 200, ExpectedResult: cfg,
	}))

	for name, bad := range map[string]*lteModels.NetworkAcsConfigs{
		"mode":     {Mode: swag.String("paused")},
		"interval": {PeriodicInformInterval: swag.Int32(-1)},
		"port":     {Port: swag.Int32(70000)},
	} {
		tc := params(tests.Test{Method: "PUT", Handler: put, Payload: bad, ExpectedStatus: 400})
		tc.ExpectedErrorSubstring = "validation failure"
		t.Run(name, func(t *testing.T) { tests.RunUnitTest(t, e, tc) })
	}

	tests.RunUnitTest(t, e, params(tests.Test{Method: "DELETE", Handler: del, ExpectedStatus: 204}))
	_, err = configurator.LoadNetworkConfig(context.Background(), "n1", lte.AcsNetworkConfigType, serdes.Network)
	assert.Equal(t, merrors.ErrNotFound, err)
}
