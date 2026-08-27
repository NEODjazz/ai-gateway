package gateway

import (
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
		Description string `json:"description,omitempty"`
		DLP         bool   `json:"dlp"`
		AV          bool   `json:"av"`
		Enabled     bool   `json:"enabled"`
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
	policy := provider.GuardrailPolicy{Description: input.Description, DLP: input.DLP, AV: input.AV, Enabled: input.Enabled}
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
	scan := modules.RequestContext{RequestID: req.RequestID, Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: input.Text}}}, Metadata: map[string]string{"provider.modules.dlp.enabled": strconv.FormatBool(policy.DLP), "provider.modules.av.enabled": strconv.FormatBool(policy.AV), "provider.guardrail.policy": policy.Name, "guardrail.monitor.source": "compliance"}}
	result := complianceResult{RequestID: req.RequestID, Policy: policy.Name, Allowed: true, Checks: map[string]string{}, ContentStored: false}
	for _, check := range []struct {
		name    string
		enabled bool
		module  modules.Module
	}{{"dlp", policy.DLP, h.dlp}, {"av", policy.AV, h.av}} {
		if !check.enabled {
			result.Checks[check.name] = "disabled"
			continue
		}
		err := check.module.Handle(r.Context(), &scan)
		if errors.Is(err, modules.ErrContentRejected) {
			result.Checks[check.name] = "rejected"
			result.Allowed = false
			continue
		}
		if err != nil {
			result.Checks[check.name] = "unavailable"
			writeJSON(w, http.StatusServiceUnavailable, result)
			return
		}
		result.Checks[check.name] = "passed"
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
