package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ai-gateway-gateway/internal/provider"
)

func (h Handler) deploymentController() (provider.DeploymentController, bool) {
	controller, ok := h.provider.(provider.DeploymentController)
	return controller, ok && controller != nil
}

func (h Handler) ListModelDeployments(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.deploymentController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment management is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": controller.ListModelDeployments(r.Context())})
}

func (h Handler) UpdateModelDeployment(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.deploymentController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment management is unavailable")
		return
	}
	var input struct {
		Models          []string `json:"models"`
		Capabilities    []string `json:"capabilities,omitempty"`
		Priority        int      `json:"priority"`
		Weight          int      `json:"weight"`
		GuardrailPolicy string   `json:"guardrail_policy,omitempty"`
		Enabled         bool     `json:"enabled"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model deployment")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model deployment")
		return
	}
	id := r.PathValue("id")
	deployment := provider.ModelDeployment{Models: input.Models, Capabilities: input.Capabilities, Priority: input.Priority, Weight: input.Weight, GuardrailPolicy: input.GuardrailPolicy, Enabled: input.Enabled}
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_deployment.update", TargetType: "model_deployment", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := controller.UpdateModelDeployment(id, deployment)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(err, provider.ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model deployment not found")
			return
		}
		if errors.Is(err, provider.ErrInvalidDeployment) {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid model deployment")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment update failed")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}
