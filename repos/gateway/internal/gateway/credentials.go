package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ai-gateway-gateway/internal/provider"
)

func (h Handler) credentialController() (provider.CredentialController, bool) {
	controller, ok := h.provider.(provider.CredentialController)
	return controller, ok && controller != nil
}

func (h Handler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.credentialController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "credential management is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": controller.ListCredentials(r.Context())})
}

func (h Handler) CreateCredential(w http.ResponseWriter, r *http.Request) {
	h.mutateCredential(w, r, "", "credential.create")
}

func (h Handler) UpdateCredential(w http.ResponseWriter, r *http.Request) {
	h.mutateCredential(w, r, r.PathValue("id"), "credential.update")
}

func (h Handler) RotateCredential(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.credentialController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "credential management is unavailable")
		return
	}
	var input struct {
		Secret string `json:"secret"`
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF || input.Secret == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid credential rotation")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "credential.rotate", TargetType: "credential", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	saved, err := controller.RotateCredential(id, input.Secret)
	input.Secret = ""
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeCredentialManagementError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, saved)
}

func (h Handler) mutateCredential(w http.ResponseWriter, r *http.Request, id, action string) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.credentialController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "credential management is unavailable")
		return
	}
	var input provider.CredentialInput
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid credential")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid credential")
		return
	}
	targetID := input.ID
	if id != "" {
		targetID = id
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: action, TargetType: "credential", TargetID: targetID}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	var saved provider.Credential
	var err error
	if id == "" {
		saved, err = controller.CreateCredential(input)
	} else {
		saved, err = controller.UpdateCredential(id, input)
	}
	input.Secret = ""
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeCredentialManagementError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, saved)
}

func (h Handler) DeleteCredential(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.credentialController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "credential management is unavailable")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "credential.delete", TargetType: "credential", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := controller.DeleteCredential(id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeCredentialManagementError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func writeCredentialManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrCredentialNotFound):
		writeError(w, http.StatusNotFound, "not_found", "credential not found")
	case errors.Is(err, provider.ErrCredentialExists):
		writeError(w, http.StatusConflict, "already_exists", "credential already exists")
	case errors.Is(err, provider.ErrCredentialInUse):
		writeError(w, http.StatusConflict, "credential_in_use", "credential is used by a model deployment")
	case errors.Is(err, provider.ErrInvalidCredential):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid credential")
	case errors.Is(err, provider.ErrControlPlaneConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; retry the request")
	default:
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "credential management failed")
	}
}
