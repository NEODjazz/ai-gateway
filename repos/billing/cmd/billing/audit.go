package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"ai-gateway-billing/internal/modules"
)

type auditManagementHandler struct {
	store   modules.AuditStore
	initErr error
	secret  string
}

func registerAuditManagement(mux *http.ServeMux, store modules.AuditStore, initErr error, secret string) {
	h := auditManagementHandler{store: store, initErr: initErr, secret: strings.TrimSpace(secret)}
	mux.HandleFunc("POST /internal/v1/audit/events", h.append)
	mux.HandleFunc("GET /internal/v1/audit/events", h.list)
}

func (h auditManagementHandler) authorize(w http.ResponseWriter, r *http.Request) bool {
	provided := r.Header.Get("X-Management-Token")
	if h.secret == "" || len(provided) != len(h.secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(h.secret)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return false
	}
	if strings.TrimSpace(r.Header.Get("X-Request-ID")) == "" || strings.TrimSpace(r.Header.Get("X-Actor-ID")) == "" || strings.TrimSpace(r.Header.Get("X-Actor-Credential-ID")) == "" {
		http.Error(w, "missing audit identity", http.StatusBadRequest)
		return false
	}
	if h.store == nil || h.initErr != nil {
		http.Error(w, "audit storage unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

type appendAuditRequest struct {
	Action     string         `json:"action"`
	TargetType string         `json:"target_type"`
	TargetID   string         `json:"target_id,omitempty"`
	Outcome    string         `json:"outcome"`
	Details    map[string]any `json:"details,omitempty"`
}

func (h auditManagementHandler) append(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	var request appendAuditRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, 32<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&request) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid audit event", http.StatusBadRequest)
		return
	}
	event, err := h.store.Append(r.Context(), modules.ManagementAuditEvent{RequestID: r.Header.Get("X-Request-ID"), ActorID: r.Header.Get("X-Actor-ID"), ActorCredentialID: r.Header.Get("X-Actor-Credential-ID"), Action: request.Action, TargetType: request.TargetType, TargetID: request.TargetID, Outcome: request.Outcome, Details: request.Details})
	if err != nil {
		if errors.Is(err, modules.ErrInvalidAuditEvent) {
			http.Error(w, "invalid audit event", http.StatusBadRequest)
			return
		}
		http.Error(w, "audit append failed", http.StatusServiceUnavailable)
		return
	}
	writeBudgetJSON(w, http.StatusCreated, event)
}

func (h auditManagementHandler) list(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	beforeRaw, limitRaw := r.URL.Query().Get("before_id"), r.URL.Query().Get("limit")
	beforeID, beforeErr := strconv.ParseInt(auditDefault(beforeRaw, "0"), 10, 64)
	limit, limitErr := strconv.Atoi(auditDefault(limitRaw, "100"))
	if beforeErr != nil || limitErr != nil || beforeID < 0 || limit < 1 || limit > 500 {
		http.Error(w, "invalid audit filter", http.StatusBadRequest)
		return
	}
	events, err := h.store.List(r.Context(), modules.AuditFilter{BeforeID: beforeID, Limit: limit, ActorID: strings.TrimSpace(r.URL.Query().Get("actor_id")), Action: strings.TrimSpace(r.URL.Query().Get("action"))})
	if err != nil {
		http.Error(w, "audit query failed", http.StatusServiceUnavailable)
		return
	}
	writeBudgetJSON(w, http.StatusOK, map[string]any{"data": events})
}

func auditDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
