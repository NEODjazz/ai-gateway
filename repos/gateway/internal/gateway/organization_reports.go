package gateway

import (
	"errors"
	"net/http"

	"ai-gateway-gateway/internal/modules"
)

// Organization administration is deliberately separate from platform admin.
// Each caller must apply the returned identity's organization to its query.
func (h Handler) authorizeOrganizationReports(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: requestID(r)}
	if err := h.pipeline.Run(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		} else {
			writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		}
		return req, false
	}
	req.APIKey = ""
	if !hasRole(req.Roles, "admin") && !(hasRole(req.Roles, "org_admin") && req.OrganizationID != "") {
		writeError(w, http.StatusForbidden, "forbidden", "platform or organization administrator is required")
		return req, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return req, true
}

func organizationReportScope(w http.ResponseWriter, req modules.RequestContext, scopeType, scopeID string) bool {
	if hasRole(req.Roles, "admin") {
		return true
	}
	if scopeType == "" && scopeID == "" || scopeType == "organization" && scopeID == req.OrganizationID {
		return true
	}
	writeError(w, http.StatusForbidden, "forbidden", "report scope must match the authenticated organization")
	return false
}
