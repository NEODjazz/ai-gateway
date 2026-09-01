package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ai-gateway-gateway/internal/provider"
)

type modelOnboardingPayload struct {
	ExpectedRevision *int64 `json:"expected_revision,omitempty"`
	provider.ModelOnboardingInput
}

func (h Handler) PlanModelOnboarding(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.provider.(provider.ModelOnboardingController)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model onboarding is unavailable")
		return
	}
	payload, ok := decodeModelOnboarding(w, r)
	if !ok {
		return
	}
	plan, err := controller.PlanModelOnboarding(r.Context(), payload.ModelOnboardingInput)
	if err != nil {
		writeModelOnboardingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

func (h Handler) ApplyModelOnboarding(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.provider.(provider.ModelOnboardingController)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model onboarding is unavailable")
		return
	}
	payload, ok := decodeModelOnboarding(w, r)
	if !ok {
		return
	}
	if payload.ExpectedRevision == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "expected_revision is required")
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_onboarding.apply", TargetType: "model_catalog", TargetID: payload.Catalog.Version}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	result, err := controller.ApplyModelOnboarding(r.Context(), *payload.ExpectedRevision, payload.ModelOnboardingInput)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeModelOnboardingError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, result)
}

func decodeModelOnboarding(w http.ResponseWriter, r *http.Request) (modelOnboardingPayload, bool) {
	var payload modelOnboardingPayload
	decoder := json.NewDecoder(io.LimitReader(r.Body, (2<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model onboarding configuration")
		return modelOnboardingPayload{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model onboarding configuration")
		return modelOnboardingPayload{}, false
	}
	return payload, true
}

func writeModelOnboardingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrControlPlaneConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; refresh the plan and retry")
	case errors.Is(err, provider.ErrDeploymentExists):
		writeError(w, http.StatusConflict, "already_exists", "a model deployment already exists")
	case errors.Is(err, provider.ErrInvalidModelOnboarding), errors.Is(err, provider.ErrInvalidDeployment), errors.Is(err, provider.ErrInvalidModelGroup):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model onboarding configuration")
	default:
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model onboarding failed")
	}
}
