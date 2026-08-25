package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"ai-gateway-gateway/internal/provider"
)

func (h Handler) providerController() (provider.ProviderController, bool) {
	controller, ok := h.provider.(provider.ProviderController)
	return controller, ok && controller != nil
}

func (h Handler) ListProviders(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.providerController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "provider management is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": controller.ListProviders(r.Context())})
}

func (h Handler) CreateProvider(w http.ResponseWriter, r *http.Request) {
	h.mutateProvider(w, r, "", "provider.create")
}

func (h Handler) UpdateProvider(w http.ResponseWriter, r *http.Request) {
	h.mutateProvider(w, r, r.PathValue("id"), "provider.update")
}

func (h Handler) mutateProvider(w http.ResponseWriter, r *http.Request, id, action string) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.providerController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "provider management is unavailable")
		return
	}
	var input provider.ManagedProvider
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid provider")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid provider")
		return
	}
	targetID := strings.TrimSpace(input.ID)
	if id != "" {
		targetID = id
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: action, TargetType: "provider", TargetID: targetID}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	var saved provider.ManagedProvider
	var err error
	if id == "" {
		saved, err = controller.CreateProvider(input)
	} else {
		saved, err = controller.UpdateProvider(id, input)
	}
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeProviderManagementError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, saved)
}

func (h Handler) DeleteProvider(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.providerController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "provider management is unavailable")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "provider.delete", TargetType: "provider", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := controller.DeleteProvider(id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeProviderManagementError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func writeProviderManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrProviderNotFound):
		writeError(w, http.StatusNotFound, "not_found", "provider not found")
	case errors.Is(err, provider.ErrProviderExists):
		writeError(w, http.StatusConflict, "already_exists", "provider already exists")
	case errors.Is(err, provider.ErrProviderInUse):
		writeError(w, http.StatusConflict, "provider_in_use", "provider is used by a model deployment")
	case errors.Is(err, provider.ErrInvalidProvider):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid provider")
	default:
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "provider management failed")
	}
}
