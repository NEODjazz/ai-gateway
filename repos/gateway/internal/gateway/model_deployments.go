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

func (h Handler) CreateModelDeployment(w http.ResponseWriter, r *http.Request) {
	h.mutateModelDeployment(w, r, "", "model_deployment.create")
}

func (h Handler) UpdateModelDeployment(w http.ResponseWriter, r *http.Request) {
	h.mutateModelDeployment(w, r, r.PathValue("id"), "model_deployment.update")
}

func (h Handler) mutateModelDeployment(w http.ResponseWriter, r *http.Request, id, action string) {
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
		ID                    string   `json:"id,omitempty"`
		ProviderID            string   `json:"provider_id,omitempty"`
		CredentialID          *string  `json:"credential_id,omitempty"`
		UpstreamModel         string   `json:"upstream_model,omitempty"`
		Models                []string `json:"models"`
		Capabilities          []string `json:"capabilities,omitempty"`
		Priority              int      `json:"priority"`
		Weight                int      `json:"weight"`
		GuardrailPolicy       string   `json:"guardrail_policy,omitempty"`
		RequestTimeoutMS      int      `json:"request_timeout_ms,omitempty"`
		MaxRetries            int      `json:"max_retries,omitempty"`
		CooldownAfterFailures int      `json:"cooldown_after_failures,omitempty"`
		CooldownSeconds       int      `json:"cooldown_seconds,omitempty"`
		MaxParallelRequests   int      `json:"max_parallel_requests,omitempty"`
		QueueCapacity         int      `json:"queue_capacity,omitempty"`
		QueueTimeoutMS        int      `json:"queue_timeout_ms,omitempty"`
		Enabled               bool     `json:"enabled"`
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
	credentialID := ""
	if input.CredentialID != nil {
		credentialID = *input.CredentialID
	}
	deployment := provider.ModelDeployment{ID: input.ID, ProviderID: input.ProviderID, CredentialID: credentialID, CredentialSet: input.CredentialID != nil, UpstreamModel: input.UpstreamModel, Models: input.Models, Capabilities: input.Capabilities, Priority: input.Priority, Weight: input.Weight, GuardrailPolicy: input.GuardrailPolicy, RequestTimeoutMS: input.RequestTimeoutMS, MaxRetries: input.MaxRetries, CooldownAfterFailures: input.CooldownAfterFailures, CooldownSeconds: input.CooldownSeconds, MaxParallelRequests: input.MaxParallelRequests, QueueCapacity: input.QueueCapacity, QueueTimeoutMS: input.QueueTimeoutMS, Enabled: input.Enabled}
	targetID := input.ID
	if id != "" {
		targetID = id
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: action, TargetType: "model_deployment", TargetID: targetID}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	var saved provider.ModelDeployment
	var err error
	if id == "" {
		saved, err = controller.CreateModelDeployment(deployment)
	} else {
		saved, err = controller.UpdateModelDeployment(id, deployment)
	}
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(err, provider.ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model deployment not found")
			return
		}
		if errors.Is(err, provider.ErrDeploymentExists) {
			writeError(w, http.StatusConflict, "already_exists", "model deployment already exists")
			return
		}
		if errors.Is(err, provider.ErrInvalidDeployment) {
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid model deployment")
			return
		}
		if errors.Is(err, provider.ErrControlPlaneConflict) {
			writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; retry the request")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment update failed")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, saved)
}

func (h Handler) DeleteModelDeployment(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.deploymentController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment management is unavailable")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_deployment.delete", TargetType: "model_deployment", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := controller.DeleteModelDeployment(id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		if errors.Is(err, provider.ErrDeploymentNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "model deployment not found")
			return
		}
		if errors.Is(err, provider.ErrDeploymentInUse) {
			writeError(w, http.StatusConflict, "deployment_in_use", "model deployment is used by a model group")
			return
		}
		if errors.Is(err, provider.ErrControlPlaneConflict) {
			writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; retry the request")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "runtime deployment deletion failed")
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}
