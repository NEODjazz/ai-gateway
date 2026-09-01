package gateway

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"ai-gateway-gateway/internal/provider"
)

func (h Handler) modelGroupController() (provider.ModelGroupController, bool) {
	controller, ok := h.provider.(provider.ModelGroupController)
	return controller, ok && controller != nil
}

func (h Handler) modelGroupRoutingController() (provider.ModelGroupRoutingController, bool) {
	controller, ok := h.provider.(provider.ModelGroupRoutingController)
	return controller, ok && controller != nil
}

func (h Handler) ListModelGroups(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.modelGroupController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model group management is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": controller.ListModelGroups(r.Context())})
}

func (h Handler) CreateModelGroup(w http.ResponseWriter, r *http.Request) {
	h.mutateModelGroup(w, r, "", "model_group.create")
}

func (h Handler) UpdateModelGroup(w http.ResponseWriter, r *http.Request) {
	h.mutateModelGroup(w, r, r.PathValue("id"), "model_group.update")
}

func (h Handler) GetModelGroupRouting(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	controller, ok := h.modelGroupRoutingController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model group routing management is unavailable")
		return
	}
	settings, err := controller.GetModelGroupRouting(r.Context(), r.PathValue("id"))
	if err != nil {
		writeModelGroupRoutingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (h Handler) UpdateModelGroupRouting(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.modelGroupRoutingController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model group routing management is unavailable")
		return
	}
	var payload struct {
		ExpectedRevision *int64 `json:"expected_revision"`
		provider.ModelGroupRoutingInput
	}
	decoder := json.NewDecoder(io.LimitReader(r.Body, 128<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF || payload.ExpectedRevision == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model group routing settings")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_group.routing.update", TargetType: "model_group", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	settings, err := controller.UpdateModelGroupRouting(r.Context(), id, provider.ModelGroupRoutingUpdate{ExpectedRevision: *payload.ExpectedRevision, ModelGroupRoutingInput: payload.ModelGroupRoutingInput})
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeModelGroupRoutingError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusOK, settings)
}

func (h Handler) mutateModelGroup(w http.ResponseWriter, r *http.Request, id, action string) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.modelGroupController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model group management is unavailable")
		return
	}
	var input provider.ModelGroup
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model group")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model group")
		return
	}
	targetID := input.ID
	if id != "" {
		targetID = id
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: action, TargetType: "model_group", TargetID: targetID}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	var saved provider.ModelGroup
	var err error
	if id == "" {
		saved, err = controller.CreateModelGroup(input)
	} else {
		saved, err = controller.UpdateModelGroup(id, input)
	}
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeModelGroupError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, saved)
}

func (h Handler) DeleteModelGroup(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	controller, ok := h.modelGroupController()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model group management is unavailable")
		return
	}
	id := r.PathValue("id")
	audit := managementAudit(req)
	event := AuditEvent{Action: "model_group.delete", TargetType: "model_group", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := controller.DeleteModelGroup(id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeModelGroupError(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func writeModelGroupError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrModelGroupNotFound):
		writeError(w, http.StatusNotFound, "not_found", "model group not found")
	case errors.Is(err, provider.ErrModelGroupExists):
		writeError(w, http.StatusConflict, "already_exists", "model group already exists")
	case errors.Is(err, provider.ErrInvalidModelGroup):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model group")
	case errors.Is(err, provider.ErrControlPlaneConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; retry the request")
	default:
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model group management failed")
	}
}

func writeModelGroupRoutingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, provider.ErrModelGroupNotFound), errors.Is(err, provider.ErrDeploymentNotFound):
		writeError(w, http.StatusNotFound, "not_found", "model group or deployment not found")
	case errors.Is(err, provider.ErrInvalidModelGroup), errors.Is(err, provider.ErrInvalidDeployment):
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid model group routing settings")
	case errors.Is(err, provider.ErrControlPlaneConflict):
		writeError(w, http.StatusConflict, "revision_conflict", "control plane changed; refresh routing settings and retry")
	default:
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "model group routing management failed")
	}
}
