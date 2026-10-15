package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"unicode"

	"ai-gateway-gateway/internal/modules"
)

// IdentityInferencePolicy narrows access independently of individual keys or
// principal bindings. A team quota applies to the verified selected team.
type IdentityInferencePolicy struct {
	ScopeType     string   `json:"scope_type"`
	ScopeID       string   `json:"scope_id"`
	ModelAccess   string   `json:"model_access"`
	AllowedModels []string `json:"allowed_models"`
	RPM           int      `json:"rpm"`
	TPM           int      `json:"tpm"`
}

type identityPolicyKey struct{ scope, id string }

func validIdentityPolicyScope(scope, id string) bool {
	return (scope == "team" || scope == "user") && len(id) > 0 && len(id) <= 256 && strings.TrimSpace(id) == id && !strings.ContainsFunc(id, unicode.IsControl)
}

func (p IdentityInferencePolicy) valid() bool {
	if !validIdentityPolicyScope(p.ScopeType, p.ScopeID) || !(GlobalRateLimitSettings{RPM: p.RPM, TPM: p.TPM}).valid() || !slices.Contains([]string{"inherit", "allowlist", "deny"}, p.ModelAccess) || len(p.AllowedModels) > 100 || (p.ModelAccess == "allowlist") != (len(p.AllowedModels) > 0) {
		return false
	}
	for _, model := range p.AllowedModels {
		if len(model) == 0 || len(model) > 512 || model != strings.TrimSpace(model) || strings.ContainsFunc(model, unicode.IsControl) || strings.ContainsAny(strings.TrimSuffix(model, "*"), "*?") {
			return false
		}
	}
	return true
}

func (p IdentityInferencePolicy) modelAllowed(model string) bool {
	return p.ModelAccess == "inherit" || p.ModelAccess == "allowlist" && modelAllowed(model, p.AllowedModels)
}

func (r *AccessRegistry) IdentityPolicy(scope, id string) (IdentityInferencePolicy, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, found := r.identityPolicies[identityPolicyKey{scope, id}]
	p.AllowedModels = append([]string{}, p.AllowedModels...)
	return p, found
}

func (r *AccessRegistry) IdentityPolicies() []IdentityInferencePolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]IdentityInferencePolicy, 0, len(r.identityPolicies))
	for _, p := range r.identityPolicies {
		p.AllowedModels = append([]string{}, p.AllowedModels...)
		result = append(result, p)
	}
	slices.SortFunc(result, func(a, b IdentityInferencePolicy) int {
		if c := strings.Compare(a.ScopeType, b.ScopeType); c != 0 {
			return c
		}
		return strings.Compare(a.ScopeID, b.ScopeID)
	})
	return result
}

func (r *AccessRegistry) PutIdentityPolicy(p IdentityInferencePolicy) error {
	if !p.valid() {
		return errInvalidAccessEntry
	}
	p.AllowedModels = uniqueStrings(p.AllowedModels)
	slices.Sort(p.AllowedModels)
	r.mu.Lock()
	defer r.mu.Unlock()
	key := identityPolicyKey{p.ScopeType, p.ScopeID}
	if _, exists := r.identityPolicies[key]; !exists && len(r.identityPolicies) >= 10_000 {
		return errors.New("identity policy capacity exceeded")
	}
	r.identityPolicies[key] = p
	return nil
}

func (r *AccessRegistry) DeleteIdentityPolicy(scope, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.identityPolicies, identityPolicyKey{scope, id})
}

func (h Handler) identityPolicies(req modules.RequestContext) []IdentityInferencePolicy {
	if h.access == nil {
		return nil
	}
	h.access.mu.RLock()
	defer h.access.mu.RUnlock()
	result := make([]IdentityInferencePolicy, 0, 2)
	for _, subject := range []identityPolicyKey{{"user", req.UserID}, {"team", req.TeamID}} {
		if p, found := h.access.identityPolicies[subject]; found {
			p.AllowedModels = append([]string{}, p.AllowedModels...)
			result = append(result, p)
		}
	}
	return result
}

func (h Handler) IdentityPolicySettings(w http.ResponseWriter, r *http.Request) {
	scope, id := r.PathValue("scope"), r.PathValue("id")
	var actor modules.RequestContext
	var ok bool
	if r.Method == http.MethodGet && scope == "team" {
		actor, ok = h.authorizeDirectory(w, r, id)
	} else {
		actor, ok = h.authorizeAdmin(w, r)
	}
	if !ok {
		return
	}
	if !validIdentityPolicyScope(scope, id) {
		writeError(w, 400, "invalid_request", "invalid identity policy scope")
		return
	}
	if h.access == nil || h.directory == nil {
		writeError(w, 503, "management_unavailable", "identity policy management is unavailable")
		return
	}
	if r.Method != http.MethodDelete && !h.identityPolicySubjectExists(w, r, actor, scope, id) {
		return
	}
	if r.Method != http.MethodGet {
		p := IdentityInferencePolicy{ScopeType: scope, ScopeID: id, ModelAccess: "inherit", AllowedModels: []string{}}
		if r.Method == http.MethodPut {
			var input struct {
				ModelAccess   string   `json:"model_access"`
				AllowedModels []string `json:"allowed_models"`
				RPM           *int     `json:"rpm"`
				TPM           *int     `json:"tpm"`
			}
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&input) != nil || input.RPM == nil || input.TPM == nil || decoder.Decode(new(any)) != io.EOF {
				writeError(w, 400, "invalid_request", "model_access, rpm and tpm are required")
				return
			}
			p.ModelAccess, p.AllowedModels, p.RPM, p.TPM = input.ModelAccess, input.AllowedModels, *input.RPM, *input.TPM
			if !p.valid() {
				writeError(w, 400, "invalid_request", "invalid identity inference policy")
				return
			}
		}
		event := AuditEvent{Action: "identity_policy.update", TargetType: scope, TargetID: id}
		audit := managementAudit(actor)
		if !h.auditMutation(r.Context(), audit, event) {
			writeError(w, 503, "audit_unavailable", "audit service is unavailable")
			return
		}
		if r.Method == http.MethodDelete {
			h.access.DeleteIdentityPolicy(scope, id)
		} else if err := h.access.PutIdentityPolicy(p); err != nil {
			h.auditOutcome(r.Context(), audit, event, "failed")
			writeError(w, 503, "management_unavailable", "could not save identity policy")
			return
		}
		h.auditOutcome(r.Context(), audit, event, "succeeded")
	}
	p, found := h.access.IdentityPolicy(scope, id)
	if !found {
		p = IdentityInferencePolicy{ScopeType: scope, ScopeID: id, ModelAccess: "inherit", AllowedModels: []string{}}
	}
	durable := false
	if h.adminState != nil {
		if controller, ok := h.adminState.controller.(interface{ AdminStateDurable() bool }); ok {
			durable = controller.AdminStateDurable()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": p, "configured": found, "durable": durable})
}

func (h Handler) identityPolicySubjectExists(w http.ResponseWriter, r *http.Request, actor modules.RequestContext, scope, id string) bool {
	audit := managementAudit(actor)
	if scope == "user" {
		user, err := h.directory.GetUser(r.Context(), audit, id)
		if err != nil {
			writeDirectoryFailure(w, err)
			return false
		}
		if user.ID == id && user.DeletedAt == nil {
			return true
		}
	} else {
		client, ok := h.directory.(TeamMetadataClient)
		if !ok {
			writeError(w, 503, "management_unavailable", "team metadata is unavailable")
			return false
		}
		team, err := client.GetTeamMetadata(r.Context(), audit, id)
		if err != nil {
			writeDirectoryFailure(w, err)
			return false
		}
		if team.ID == id && team.DeletedAt == nil {
			return true
		}
	}
	writeError(w, 404, "not_found", "identity was not found")
	return false
}

func (h Handler) prepareIdentityPolicies(ctx context.Context, w http.ResponseWriter, req *modules.RequestContext) bool {
	if h.adminState != nil {
		if err := h.adminState.Refresh(ctx); err != nil {
			writeError(w, 503, "access_policy_unavailable", "identity policy is unavailable")
			return false
		}
	}
	policies := h.identityPolicies(*req)
	if len(policies) > 0 || req.Metadata["policy.identity"] != "" {
		req.Metadata = maps.Clone(req.Metadata)
		delete(req.Metadata, "policy.identity")
	}
	if len(policies) > 0 {
		data, err := json.Marshal(policies)
		if err != nil {
			writeError(w, 503, "access_policy_unavailable", "identity policy is unavailable")
			return false
		}
		if req.Metadata == nil {
			req.Metadata = make(map[string]string)
		}
		req.Metadata["policy.identity"] = string(data)
	}
	return true
}
