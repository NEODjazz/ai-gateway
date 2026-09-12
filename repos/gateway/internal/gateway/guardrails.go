package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func (h Handler) guardrailController() (provider.GuardrailController, bool) {
	controller, ok := h.provider.(provider.GuardrailController)
	return controller, ok && controller != nil
}
func (h Handler) WithComplianceModules(dlp, av modules.Module) Handler {
	h.dlp, h.av = dlp, av
	return h
}

func (h Handler) ListGuardrailPolicies(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.guardrailController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "guardrail management is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": controller.ListGuardrailPolicies()})
}
func (h Handler) UpdateGuardrailPolicy(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.guardrailController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "guardrail management is unavailable")
		return
	}
	var input struct {
		Description        string   `json:"description,omitempty"`
		DLP                bool     `json:"dlp"`
		OutputDLP          bool     `json:"output_dlp"`
		AV                 bool     `json:"av"`
		Anonymization      string   `json:"anonymization,omitempty"`
		AnonymizationRules []string `json:"anonymization_rules,omitempty"`
		Enabled            bool     `json:"enabled"`
	}
	if !decodeGuardrailJSON(w, r, &input) {
		return
	}
	name := r.PathValue("name")
	audit := managementAudit(req)
	event := AuditEvent{Action: "guardrail_policy.update", TargetType: "guardrail_policy", TargetID: name}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	policy := provider.GuardrailPolicy{Description: input.Description, DLP: input.DLP, OutputDLP: input.OutputDLP, AV: input.AV, Anonymization: input.Anonymization, AnonymizationRules: input.AnonymizationRules, Enabled: input.Enabled}
	var saved provider.GuardrailPolicy
	var err error
	if durable, ok := h.provider.(provider.DurableGuardrailController); ok {
		saved, err = durable.UpdateGuardrailPolicyDurable(r.Context(), name, policy)
	} else {
		saved, err = controller.UpdateGuardrailPolicy(name, policy)
	}
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(err, provider.ErrControlPlaneConflict) {
			writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; refresh and retry")
		} else {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid guardrail policy")
		}
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}

type complianceResult struct {
	RequestID     string            `json:"request_id"`
	Policy        string            `json:"policy"`
	Allowed       bool              `json:"allowed"`
	Checks        map[string]string `json:"checks"`
	ContentStored bool              `json:"content_stored"`
}

type guardrailApplyResult struct {
	ExecutionID   string            `json:"execution_id"`
	GuardrailName string            `json:"guardrail_name"`
	Allowed       bool              `json:"allowed"`
	Checks        map[string]string `json:"checks"`
	ContentStored bool              `json:"content_stored"`
}

func (h Handler) ApplyGuardrail(w http.ResponseWriter, r *http.Request) {
	var input struct {
		GuardrailName string `json:"guardrail_name"`
		Text          string `json:"text"`
		Model         string `json:"model,omitempty"`
	}
	if !decodeGuardrailJSON(w, r, &input) {
		return
	}
	input.GuardrailName = strings.TrimSpace(input.GuardrailName)
	input.Model = strings.TrimSpace(input.Model)
	if !validGuardrailName(input.GuardrailName) || input.Text == "" || len(input.Text) > 64<<10 || len(input.Model) > 512 {
		writeError(w, http.StatusBadRequest, "invalid_request", "guardrail_name, model or text is invalid")
		return
	}
	req := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: executionID(w),
		Request:   openai.ChatCompletionRequest{Model: input.Model, Messages: []openai.Message{{Role: "user", Content: input.Text}}},
	}
	if err := h.pipeline.RunAuthentication(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
		return
	}
	if !h.prepareAccessGroups(w, &req) {
		return
	}
	tokens := openai.EstimateContextTokens(input.Text)
	if input.Model != "" {
		if !h.authorizeAccess(w, r.Context(), req, input.Model, tokens) {
			return
		}
	} else if !h.authorizeRateLimit(w, r.Context(), req, tokens) {
		return
	}
	if !h.authorizeGuardrailPolicy(w, req, input.GuardrailName, input.Model) {
		return
	}
	controller, ok := h.guardrailController()
	if !ok || h.dlp == nil || h.av == nil {
		writeError(w, http.StatusServiceUnavailable, "guardrail_unavailable", "guardrail execution is unavailable")
		return
	}
	policy, found := controller.GetGuardrailPolicy(input.GuardrailName)
	if !found || !policy.Enabled {
		writeError(w, http.StatusBadRequest, "invalid_request", "guardrail policy is missing or disabled")
		return
	}
	if h.audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is not configured")
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "guardrail.apply", TargetType: "guardrail_policy", TargetID: policy.Name}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	checks, allowed, err := h.executeGuardrail(r.Context(), req.RequestID, "guardrail_api", input.Text, policy)
	event.Details = map[string]any{"allowed": allowed, "checks": checks}
	event.Outcome = "succeeded"
	if err != nil {
		event.Outcome = "failed"
	}
	if _, auditErr := h.audit.AppendAudit(r.Context(), audit, event); auditErr != nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "guardrail_unavailable", "guardrail execution is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, guardrailApplyResult{ExecutionID: req.RequestID, GuardrailName: policy.Name, Allowed: allowed, Checks: checks, ContentStored: false})
}

func validGuardrailName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' && character != '_' && character != '.' {
			return false
		}
	}
	return true
}

func (h Handler) authorizeGuardrailPolicy(w http.ResponseWriter, req modules.RequestContext, name, model string) bool {
	if hasRole(req.Roles, "admin") {
		return true
	}
	if h.access == nil {
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable", "policy attachment registry is unavailable")
		return false
	}
	match := PolicyMatchContext{TeamID: req.TeamID, CredentialID: req.CredentialID, CredentialAlias: req.CredentialAlias, Model: model, Tags: req.Tags}
	resolution := h.resolvePolicyAttachmentSet(h.access.MatchingPolicyAttachments(match), match)
	if !resolution.Enforceable {
		writeError(w, http.StatusServiceUnavailable, "policy_unavailable", "an attached policy is missing or disabled")
		return false
	}
	for _, policy := range resolution.EffectivePolicies {
		if policy == name {
			return true
		}
	}
	writeError(w, http.StatusForbidden, "guardrail_not_allowed", "credential is not allowed to execute this guardrail")
	return false
}

func (h Handler) executeGuardrail(ctx context.Context, requestID, source, text string, policy provider.GuardrailPolicy) (map[string]string, bool, error) {
	scan := modules.RequestContext{RequestID: requestID, Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: text}}}, Metadata: map[string]string{
		"provider.modules.dlp.enabled": strconv.FormatBool(policy.DLP),
		"provider.modules.av.enabled":  strconv.FormatBool(policy.AV),
		"provider.guardrail.policy":    policy.Name,
		"guardrail.monitor.source":     source,
	}}
	checks := map[string]string{}
	allowed := true
	for _, check := range []struct {
		name    string
		enabled bool
		module  modules.Module
	}{{"dlp", policy.DLP, h.dlp}, {"av", policy.AV, h.av}} {
		if !check.enabled {
			checks[check.name] = "disabled"
			continue
		}
		err := check.module.Handle(ctx, &scan)
		if errors.Is(err, modules.ErrContentRejected) {
			checks[check.name] = "rejected"
			allowed = false
			continue
		}
		if err != nil {
			checks[check.name] = "unavailable"
			return checks, false, err
		}
		checks[check.name] = "passed"
	}
	return checks, allowed, nil
}

func (h Handler) CheckCompliance(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.guardrailController()
	if !ok || h.dlp == nil || h.av == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "compliance checks are unavailable")
		return
	}
	var input struct {
		Policy string `json:"policy"`
		Text   string `json:"text"`
	}
	if !decodeGuardrailJSON(w, r, &input) {
		return
	}
	input.Policy = strings.TrimSpace(input.Policy)
	if input.Text == "" || len(input.Text) > 64<<10 {
		writeError(w, http.StatusBadRequest, "invalid_request", "text must contain 1 to 65536 bytes")
		return
	}
	policy, found := controller.GetGuardrailPolicy(input.Policy)
	if !found || !policy.Enabled {
		writeError(w, http.StatusBadRequest, "invalid_request", "guardrail policy is missing or disabled")
		return
	}
	checks, allowed, err := h.executeGuardrail(r.Context(), req.RequestID, "compliance", input.Text, policy)
	result := complianceResult{RequestID: req.RequestID, Policy: policy.Name, Allowed: allowed, Checks: checks, ContentStored: false}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, result)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
func decodeGuardrailJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid guardrail request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid guardrail request")
		return false
	}
	return true
}
