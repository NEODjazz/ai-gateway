package gateway

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"ai-gateway-gateway/internal/provider"
)

func (h Handler) deploymentHealthController() (provider.DeploymentHealthController, bool) {
	controller, ok := h.provider.(provider.DeploymentHealthController)
	return controller, ok && controller != nil
}

func (h Handler) TestModelDeployment(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.deploymentHealthController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health checks are unavailable")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_deployment.test", TargetType: "model_deployment", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	check, err := controller.TestModelDeployment(ctx, id)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeDeploymentHealthError(w, err)
		return
	}
	outcome := "succeeded"
	if check.Status != "available" {
		outcome = "failed"
	}
	h.auditOutcome(r.Context(), audit, event, outcome)
	writeJSON(w, http.StatusOK, check)
}

func (h Handler) ListModelDeploymentHealth(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.deploymentHealthController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health checks are unavailable")
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 50 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 50")
			return
		}
		limit = parsed
	}
	checks, err := controller.ListModelDeploymentHealth(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeDeploymentHealthError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": checks})
}

func writeDeploymentHealthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrDeploymentNotFound):
		writeError(w, http.StatusNotFound, "not_found", "model deployment not found")
	case errors.Is(err, provider.ErrInvalidDeployment):
		writeError(w, http.StatusBadRequest, "invalid_deployment", "model deployment cannot be tested")
	default:
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "deployment health check failed")
	}
}
