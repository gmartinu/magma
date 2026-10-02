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
// Package handlers serves the entitlements REST API.
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-openapi/strfmt"
	"github.com/go-openapi/swag"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"magma/orc8r/cloud/go/services/entitlements"
	"magma/orc8r/cloud/go/services/entitlements/obsidian/models"
	"magma/orc8r/cloud/go/services/entitlements/protos"
	"magma/orc8r/cloud/go/services/obsidian"
	"magma/orc8r/lib/go/merrors"
)

const (
	TenantEntitlementsPath  = obsidian.V1Root + "tenants/:tenant_id/entitlements"
	TenantEntitlementPath   = TenantEntitlementsPath + obsidian.UrlSep + ":feature"
	NetworkEntitlementsPath = obsidian.V1Root + "networks/:network_id/entitlements"
)

func GetObsidianHandlers() []obsidian.Handler {
	return []obsidian.Handler{
		{Path: TenantEntitlementsPath, Methods: obsidian.GET, HandlerFunc: listTenantEntitlements},
		{Path: TenantEntitlementsPath, Methods: obsidian.PUT, HandlerFunc: replaceTenantEntitlements},
		{Path: TenantEntitlementPath, Methods: obsidian.GET, HandlerFunc: getTenantEntitlement},
		{Path: TenantEntitlementPath, Methods: obsidian.PUT, HandlerFunc: setTenantEntitlement},
		{Path: TenantEntitlementPath, Methods: obsidian.DELETE, HandlerFunc: deleteTenantEntitlement},
		{Path: NetworkEntitlementsPath, Methods: obsidian.GET, HandlerFunc: getNetworkEntitlements},
	}
}

func listTenantEntitlements(c echo.Context) error {
	tenantID, herr := obsidian.GetTenantID(c)
	if herr != nil {
		return herr
	}
	ents, err := entitlements.ListEntitlements(c.Request().Context(), tenantID)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, toModels(ents, time.Now()))
}

func replaceTenantEntitlements(c echo.Context) error {
	tenantID, herr := obsidian.GetTenantID(c)
	if herr != nil {
		return herr
	}
	var payload []*models.Entitlement
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		return obsidian.MakeHTTPError(fmt.Errorf("error decoding request: %w", err), http.StatusBadRequest)
	}
	ents := make([]*protos.Entitlement, 0, len(payload))
	for _, m := range payload {
		e, herr := fromModel(m)
		if herr != nil {
			return herr
		}
		ents = append(ents, e)
	}
	if err := entitlements.SetEntitlements(c.Request().Context(), tenantID, ents, true); err != nil {
		return httpError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func getTenantEntitlement(c echo.Context) error {
	tenantID, feature, herr := tenantAndFeature(c)
	if herr != nil {
		return herr
	}
	e, err := entitlements.GetEntitlement(c.Request().Context(), tenantID, feature)
	if err != nil {
		return httpError(err)
	}
	return c.JSON(http.StatusOK, toModel(e, time.Now()))
}

func setTenantEntitlement(c echo.Context) error {
	tenantID, feature, herr := tenantAndFeature(c)
	if herr != nil {
		return herr
	}
	m := &models.Entitlement{}
	if err := json.NewDecoder(c.Request().Body).Decode(m); err != nil {
		return obsidian.MakeHTTPError(fmt.Errorf("error decoding request: %w", err), http.StatusBadRequest)
	}
	if m.Feature == "" {
		m.Feature = feature
	}
	if m.Feature != feature {
		return obsidian.MakeHTTPError(fmt.Errorf("feature %q in the body does not match %q in the path", m.Feature, feature), http.StatusBadRequest)
	}
	e, herr := fromModel(m)
	if herr != nil {
		return herr
	}
	if err := entitlements.SetEntitlements(c.Request().Context(), tenantID, []*protos.Entitlement{e}, false); err != nil {
		return httpError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func deleteTenantEntitlement(c echo.Context) error {
	tenantID, feature, herr := tenantAndFeature(c)
	if herr != nil {
		return herr
	}
	if err := entitlements.DeleteEntitlement(c.Request().Context(), tenantID, feature); err != nil {
		return httpError(err)
	}
	return c.NoContent(http.StatusNoContent)
}

func getNetworkEntitlements(c echo.Context) error {
	networkID, herr := obsidian.GetNetworkId(c)
	if herr != nil {
		return herr
	}
	ne, err := entitlements.GetNetworkEntitlements(c.Request().Context(), networkID)
	if err != nil {
		return httpError(err)
	}
	res := &models.NetworkEntitlements{
		NetworkID:    ne.NetworkId,
		Enforce:      ne.Enforce,
		Entitlements: toModels(ne.Entitlements, time.Now()),
	}
	if ne.HasTenant {
		res.TenantID = swag.Int64(ne.TenantId)
	}
	return c.JSON(http.StatusOK, res)
}

func tenantAndFeature(c echo.Context) (int64, string, *echo.HTTPError) {
	tenantID, herr := obsidian.GetTenantID(c)
	if herr != nil {
		return 0, "", herr
	}
	feature := c.Param("feature")
	if feature == "" {
		return 0, "", obsidian.MakeHTTPError(fmt.Errorf("missing feature"), http.StatusBadRequest)
	}
	return tenantID, feature, nil
}

func fromModel(m *models.Entitlement) (*protos.Entitlement, *echo.HTTPError) {
	if m == nil {
		return nil, obsidian.MakeHTTPError(fmt.Errorf("null entitlement"), http.StatusBadRequest)
	}
	// state and updated_at are read-only: ignore what the client sends.
	m.State, m.UpdatedAt = "", strfmt.DateTime{}
	if err := m.Validate(strfmt.Default); err != nil {
		return nil, obsidian.MakeHTTPError(err, http.StatusBadRequest)
	}
	e := &protos.Entitlement{
		Feature:   m.Feature,
		Enabled:   swag.BoolValue(m.Enabled),
		GraceDays: entitlements.DefaultGraceDays,
		Source:    swag.StringValue(m.Source),
		LicenseId: m.LicenseID,
	}
	if m.GraceDays != nil {
		e.GraceDays = *m.GraceDays
	}
	if m.NotAfter != nil {
		e.NotAfter = time.Time(*m.NotAfter).Unix()
	}
	return e, nil
}

func toModel(e *protos.Entitlement, now time.Time) *models.Entitlement {
	m := &models.Entitlement{
		Feature:   e.Feature,
		Enabled:   swag.Bool(e.Enabled),
		GraceDays: swag.Int32(e.GraceDays),
		Source:    swag.String(e.Source),
		LicenseID: e.LicenseId,
		UpdatedAt: strfmt.DateTime(time.Unix(e.UpdatedAt, 0).UTC()),
		State:     models.EntitlementState(entitlements.State(e, now)),
	}
	if e.NotAfter != 0 {
		notAfter := strfmt.DateTime(time.Unix(e.NotAfter, 0).UTC())
		m.NotAfter = &notAfter
	}
	return m
}

func toModels(ents []*protos.Entitlement, now time.Time) []*models.Entitlement {
	res := make([]*models.Entitlement, 0, len(ents))
	for _, e := range ents {
		res = append(res, toModel(e, now))
	}
	return res
}

func httpError(err error) error {
	switch {
	case err == merrors.ErrNotFound:
		return obsidian.MakeHTTPError(err, http.StatusNotFound)
	case status.Code(err) == codes.InvalidArgument:
		return obsidian.MakeHTTPError(fmt.Errorf("%s", status.Convert(err).Message()), http.StatusBadRequest)
	default:
		return obsidian.MakeHTTPError(err, http.StatusInternalServerError)
	}
}
