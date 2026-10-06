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

package handlers

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/labstack/echo/v4"

	"magma/acs/cloud/go/acs"
	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/lte/cloud/go/lte"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/configurator"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/obsidian/guard"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/cloud/go/services/state"
	state_types "magma/orc8r/cloud/go/services/state/types"
	"magma/orc8r/lib/go/merrors"
)

const (
	ManageNetworkPath = obsidian.V1Root + acs.ModuleName + obsidian.UrlSep + ":network_id"
	CpesPath          = ManageNetworkPath + obsidian.UrlSep + "cpes"
	CpePath           = CpesPath + obsidian.UrlSep + ":cpe_key"

	// magmad reports state every minute; past this the AGW is taken as
	// gone and its CPEs as offline, whatever acsd last said.
	stateStaleAfter = 10 * time.Minute
)

type Handlers struct {
	cpes CpeManagers
	logs LogSearcher
	now  func() time.Time
}

func NewHandlers(cpes CpeManagers) *Handlers {
	return &Handlers{cpes: cpes, now: time.Now}
}

func (h *Handlers) GetHandlers() []obsidian.Handler {
	return guardAll(append([]obsidian.Handler{
		{Path: CpesPath, Methods: obsidian.GET, HandlerFunc: h.listCpes},
		{Path: CpePath, Methods: obsidian.GET, HandlerFunc: h.getCpe},
		{Path: LogsPath, Methods: obsidian.GET, HandlerFunc: h.searchLogs},
	}, h.relayHandlers()...))
}

// requireFeature is the one gate every acs route passes: the network must
// be entitled to acs (a pass when the deployment does not enforce). A var
// so tests can prove every route uses it.
var requireFeature = func(c echo.Context) error {
	return guard.CheckEntitlement(c, entitlements.FeatureACS)
}

func guardAll(hs []obsidian.Handler) []obsidian.Handler {
	for i := range hs {
		next := hs[i].HandlerFunc
		hs[i].HandlerFunc = func(c echo.Context) error {
			if err := requireFeature(c); err != nil {
				return err
			}
			return next(c)
		}
	}
	return hs
}

func (h *Handlers) listCpes(c echo.Context) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	ctx := c.Request().Context()
	states, err := state.SearchStates(ctx, networkID, []string{lte.CPEAcsStateType}, nil, nil, cpestate.Serdes)
	if err != nil {
		return obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}
	gateways, err := gatewayIDsByHwID(ctx, networkID)
	if err != nil {
		return obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}

	var online *bool
	if v := c.QueryParam("online"); v != "" {
		b := v == "true"
		if !b && v != "false" {
			return echo.NewHTTPError(http.StatusBadRequest, "online must be true or false")
		}
		online = &b
	}
	model, gatewayID, mode := c.QueryParam("model"), c.QueryParam("gateway_id"), c.QueryParam("mode")

	out := []*models.AcsCpe{}
	for _, st := range states {
		view, ok := st.ReportedState.(*cpestate.CpeView)
		if !ok {
			continue
		}
		cpe := h.toCpe(view, st, gateways)
		if model != "" && model != cpe.ModelName && model != cpe.ProductClass {
			continue
		}
		if online != nil && *online != cpe.Online {
			continue
		}
		if gatewayID != "" && gatewayID != cpe.GatewayID {
			continue
		}
		if mode != "" && mode != cpe.Mode {
			continue
		}
		out = append(out, cpe)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CpeKey < out[j].CpeKey })
	return c.JSON(http.StatusOK, out)
}

func (h *Handlers) getCpe(c echo.Context) error {
	networkID, nerr := obsidian.GetNetworkId(c)
	if nerr != nil {
		return nerr
	}
	ctx := c.Request().Context()
	view, st, err := loadCpe(ctx, networkID, c.Param("cpe_key"))
	if err != nil {
		return err
	}
	gateways, err := gatewayIDsByHwID(ctx, networkID)
	if err != nil {
		return obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}
	return c.JSON(http.StatusOK, &models.AcsCpeDetail{Cpe: *h.toCpe(view, st, gateways), Model: view.Model})
}

// loadCpe reads a CPE's state; its ReporterID is the gateway serving it,
// so the state store doubles as the cpe_key -> gateway index.
func loadCpe(ctx context.Context, networkID, cpeKey string) (*cpestate.CpeView, state_types.State, error) {
	st, err := state.GetState(ctx, networkID, lte.CPEAcsStateType, cpeKey, cpestate.Serdes)
	if errors.Is(err, merrors.ErrNotFound) {
		return nil, st, echo.NewHTTPError(http.StatusNotFound, "no gateway reports CPE "+cpeKey)
	}
	if err != nil {
		return nil, st, obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}
	view, ok := st.ReportedState.(*cpestate.CpeView)
	if !ok {
		return nil, st, echo.NewHTTPError(http.StatusInternalServerError, "malformed cpe_acs state")
	}
	return view, st, nil
}

func gatewayIDsByHwID(ctx context.Context, networkID string) (map[string]string, error) {
	out := map[string]string{}
	criteria := configurator.EntityLoadCriteria{}
	for {
		ents, next, err := configurator.LoadAllEntitiesOfType(ctx, networkID, orc8r.MagmadGatewayType, criteria, nil)
		if err != nil {
			return nil, err
		}
		for _, e := range ents {
			out[e.PhysicalID] = e.Key
		}
		if next == "" {
			return out, nil
		}
		criteria.PageToken = next
	}
}

func (h *Handlers) toCpe(v *cpestate.CpeView, st state_types.State, gateways map[string]string) *models.AcsCpe {
	reportedAt := time.UnixMilli(int64(st.TimeMs)).UTC()
	cpe := &models.AcsCpe{
		CpeKey:          v.CpeKey,
		Mode:            v.Mode,
		Imsi:            v.Imsi,
		SerialNumber:    v.SerialNumber,
		Oui:             v.Oui,
		ProductClass:    v.ProductClass,
		ModelName:       v.ModelName(),
		SoftwareVersion: v.SoftwareVersion,
		Handler:         v.Handler,
		LastInform:      unixTime(v.LastInform),
		InformsTotal:    v.InformsTotal,
		Online:          v.Online && h.now().Sub(reportedAt) <= stateStaleAfter,
		PendingTasks:    v.PendingTasks,
		GatewayID:       gateways[st.ReporterID],
		HardwareID:      st.ReporterID,
		ReportedAt:      strfmt.DateTime(reportedAt),
	}
	if cpe.Mode == "" {
		cpe.Mode = cpestate.ModeCore
	}
	if s := v.LastSession; s != nil {
		cpe.LastSession = &models.AcsCpeSession{
			SessionID:   s.SessionID,
			Result:      s.Result,
			Reason:      s.Reason,
			Started:     unixTime(s.Started),
			Ended:       unixTime(s.Ended),
			TasksDone:   s.TasksDone,
			TasksFailed: s.TasksFailed,
			Faults:      s.Faults,
		}
	}
	return cpe
}

func unixTime(sec float64) *strfmt.DateTime {
	if sec <= 0 {
		return nil
	}
	t := strfmt.DateTime(time.UnixMilli(int64(sec * 1000)).UTC())
	return &t
}
