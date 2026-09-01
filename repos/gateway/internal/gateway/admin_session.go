package gateway

import (
	"errors"
	"net/http"
	"sort"

	"ai-gateway-gateway/internal/modules"
)

const (
	consoleCapabilityAdmin         = "admin"
	consoleCapabilityAPIDocs       = "api_docs"
	consoleCapabilityInference     = "inference"
	consoleCapabilityTeamDirectory = "team_directory"
)

type AdminSession struct {
	UserID          string   `json:"user_id,omitempty"`
	TeamID          string   `json:"team_id,omitempty"`
	OrganizationID  string   `json:"organization_id,omitempty"`
	CredentialID    string   `json:"credential_id,omitempty"`
	CredentialAlias string   `json:"credential_alias,omitempty"`
	Roles           []string `json:"roles"`
	AllowedModels   []string `json:"allowed_models"`
	AllowedTools    []string `json:"allowed_tools"`
	Capabilities    []string `json:"capabilities"`
}

// GetAdminSession validates the presented gateway credential and exposes only
// the bounded identity and capability metadata needed by the management UI.
func (h Handler) GetAdminSession(w http.ResponseWriter, r *http.Request) {
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: requestID(r)}
	if err := h.pipeline.Run(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		} else {
			writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		}
		return
	}
	req.APIKey = ""
	writeJSON(w, http.StatusOK, AdminSession{
		UserID:          req.UserID,
		TeamID:          req.TeamID,
		OrganizationID:  req.OrganizationID,
		CredentialID:    req.CredentialID,
		CredentialAlias: req.CredentialAlias,
		Roles:           uniqueSorted(req.Roles),
		AllowedModels:   uniqueSorted(req.AllowedModels),
		AllowedTools:    uniqueSorted(req.AllowedTools),
		Capabilities:    consoleCapabilities(req.Roles),
	})
}

func consoleCapabilities(roles []string) []string {
	capabilities := []string{consoleCapabilityAPIDocs, consoleCapabilityInference}
	if hasRole(roles, "admin") {
		capabilities = append(capabilities, consoleCapabilityAdmin, consoleCapabilityTeamDirectory)
	} else if hasRole(roles, "team_admin") {
		capabilities = append(capabilities, consoleCapabilityTeamDirectory)
	}
	return uniqueSorted(capabilities)
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
