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
	"net/http"
	"sort"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/labstack/echo/v4"

	"magma/acs/cloud/go/acs"
	acs_service "magma/acs/cloud/go/services/acs"
	"magma/acs/cloud/go/services/acs/cpestate"
	"magma/acs/cloud/go/services/acs/obsidian/models"
	"magma/acs/cloud/go/services/acs/reports"
	"magma/orc8r/cloud/go/orc8r"
	"magma/orc8r/cloud/go/services/configurator"
	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/obsidian/guard"
	"magma/orc8r/cloud/go/services/obsidian"
)

const (
	ManageNetworkPath = obsidian.V1Root + acs.ModuleName + obsidian.UrlSep + ":network_id"
	CpesPath          = ManageNetworkPath + obsidian.UrlSep + "cpes"
	CpePath           = CpesPath + obsidian.UrlSep + ":cpe_key"

	// magmad reports state every minute; past this the AGW is taken as
	// gone and its CPEs as offline, whatever acsd last said.
	stateStaleAfter = 10 * time.Minute

	defaultStaleCpeAfter = acs_service.DefaultStaleCpeAfterHours * time.Hour
)

// CpeReports reads what each gateway reports of the CPEs; reports.Store
// in production.
type CpeReports interface {
	List(networkID string) (map[string][]reports.Report, error)
	Get(networkID, cpeKey string) ([]reports.Report, error)
}

type Handlers struct {
	cpes          CpeManagers
	reports       CpeReports
	logs          LogSearcher
	now           func() time.Time
	staleCpeAfter time.Duration
}

func NewHandlers(cpes CpeManagers, cpeReports CpeReports) *Handlers {
	return &Handlers{cpes: cpes, reports: cpeReports, now: time.Now, staleCpeAfter: defaultStaleCpeAfter}
}

// WithStaleCpeAfter sets how old a CPE's last Inform may be for the CPE
// list to show it by default; 0 or less keeps the default (7 days).
func (h *Handlers) WithStaleCpeAfter(d time.Duration) *Handlers {
	if d > 0 {
		h.staleCpeAfter = d
	}
	return h
}

// stale reports whether r's CPE has been silent past the horizon. Nothing
// deletes a cpe_acs row from Orc8r (acsd stops reporting a CPE after 7
// days, the cloud keeps the last report), so the list hides such rows.
func (h *Handlers) stale(r reports.Report) bool {
	last := time.UnixMilli(int64(r.View.LastInform * 1000))
	if r.View.LastInform <= 0 {
		last = time.UnixMilli(int64(r.TimeMs))
	}
	return h.now().Sub(last) > h.staleCpeAfter
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
	byKey, err := h.reports.List(networkID)
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
	includeStale := false
	if v := c.QueryParam("include_stale"); v != "" {
		if v != "true" && v != "false" {
			return echo.NewHTTPError(http.StatusBadRequest, "include_stale must be true or false")
		}
		includeStale = v == "true"
	}
	model, gatewayID, mode := c.QueryParam("model"), c.QueryParam("gateway_id"), c.QueryParam("mode")

	out := []*models.AcsCpe{}
	for _, rs := range byKey {
		if len(rs) == 0 {
			continue
		}
		owner, others := reports.Resolve(rs)
		if !includeStale && h.stale(owner) {
			continue
		}
		cpe := h.toCpe(owner, others, gateways)
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
	owner, others, err := h.loadCpe(networkID, c.Param("cpe_key"))
	if err != nil {
		return err
	}
	gateways, err := gatewayIDsByHwID(ctx, networkID)
	if err != nil {
		return obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}
	return c.JSON(http.StatusOK, &models.AcsCpeDetail{Cpe: *h.toCpe(owner, others, gateways), Model: owner.View.Model})
}

// loadCpe reads what the gateways report of a CPE and resolves the one
// serving it (reports.Resolve).
func (h *Handlers) loadCpe(networkID, cpeKey string) (reports.Report, []reports.Report, error) {
	rs, err := h.reports.Get(networkID, cpeKey)
	if err != nil {
		return reports.Report{}, nil, obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}
	if len(rs) == 0 {
		return reports.Report{}, nil, echo.NewHTTPError(http.StatusNotFound, "no gateway reports CPE "+cpeKey)
	}
	owner, others := reports.Resolve(rs)
	return owner, others, nil
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

// online is the CPE's online flag as r's gateway reports it, false once
// that report is stale.
func (h *Handlers) online(r reports.Report) bool {
	return r.View.Online && h.now().Sub(time.UnixMilli(int64(r.TimeMs))) <= stateStaleAfter
}

func (h *Handlers) toCpe(owner reports.Report, others []reports.Report, gateways map[string]string) *models.AcsCpe {
	v := owner.View
	reportedAt := time.UnixMilli(int64(owner.TimeMs)).UTC()
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
		Online:          h.online(owner),
		PendingTasks:    v.PendingTasks,
		GatewayID:       gateways[owner.HardwareID],
		HardwareID:      owner.HardwareID,
		ReportedAt:      strfmt.DateTime(reportedAt),
	}
	// Claim IDs are chosen on each AGW (acsd_cli.py), so two AGWs can claim
	// different CPEs under one key; Orc8r cannot refuse that, but it can show
	// it.
	for _, o := range others {
		if !h.online(o) {
			continue
		}
		id := gateways[o.HardwareID]
		if id == "" {
			id = o.HardwareID
		}
		cpe.ConflictingGatewayIds = append(cpe.ConflictingGatewayIds, id)
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
