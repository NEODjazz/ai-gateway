package gateway

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type AuditEvent struct {
	ID                int64          `json:"id,omitempty"`
	OccurredAt        time.Time      `json:"occurred_at,omitempty"`
	RequestID         string         `json:"request_id,omitempty"`
	ActorID           string         `json:"actor_id,omitempty"`
	ActorCredentialID string         `json:"actor_credential_id,omitempty"`
	Action            string         `json:"action"`
	TargetType        string         `json:"target_type"`
	TargetID          string         `json:"target_id,omitempty"`
	Outcome           string         `json:"outcome"`
	Details           map[string]any `json:"details,omitempty"`
}

type AuditFilter struct {
	BeforeID        int64
	Limit           int
	ActorID, Action string
}
type AuditClient interface {
	AppendAudit(context.Context, ManagementAudit, AuditEvent) (AuditEvent, error)
	ListAudit(context.Context, ManagementAudit, AuditFilter) ([]AuditEvent, error)
}

func (c *RemoteBudgetManagementClient) AppendAudit(ctx context.Context, audit ManagementAudit, event AuditEvent) (AuditEvent, error) {
	var result AuditEvent
	err := c.call(ctx, http.MethodPost, "/internal/v1/audit/events", audit, event, &result)
	return result, err
}
func (c *RemoteBudgetManagementClient) ListAudit(ctx context.Context, audit ManagementAudit, filter AuditFilter) ([]AuditEvent, error) {
	query := url.Values{}
	if filter.BeforeID > 0 {
		query.Set("before_id", strconv.FormatInt(filter.BeforeID, 10))
	}
	if filter.Limit > 0 {
		query.Set("limit", strconv.Itoa(filter.Limit))
	}
	if filter.ActorID != "" {
		query.Set("actor_id", filter.ActorID)
	}
	if filter.Action != "" {
		query.Set("action", filter.Action)
	}
	path := "/internal/v1/audit/events"
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var result struct {
		Data []AuditEvent `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, path, audit, nil, &result)
	return result.Data, err
}

func (h Handler) WithAudit(client AuditClient) Handler { h.audit = client; return h }

func (h Handler) ListAuditEvents(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is not configured")
		return
	}
	beforeID, err := strconv.ParseInt(defaultString(r.URL.Query().Get("before_id"), "0"), 10, 64)
	if err != nil || beforeID < 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid audit filter")
		return
	}
	limit, err := strconv.Atoi(defaultString(r.URL.Query().Get("limit"), "100"))
	if err != nil || limit < 1 || limit > 500 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid audit filter")
		return
	}
	events, err := h.audit.ListAudit(r.Context(), managementAudit(req), AuditFilter{BeforeID: beforeID, Limit: limit, ActorID: strings.TrimSpace(r.URL.Query().Get("actor_id")), Action: strings.TrimSpace(r.URL.Query().Get("action"))})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": events})
}

func (h Handler) auditMutation(ctx context.Context, audit ManagementAudit, event AuditEvent) bool {
	if h.audit == nil {
		return true
	}
	event.Outcome = "attempted"
	if _, err := h.audit.AppendAudit(ctx, audit, event); err != nil {
		return false
	}
	return true
}
func (h Handler) auditOutcome(ctx context.Context, audit ManagementAudit, event AuditEvent, outcome string) {
	if h.audit == nil {
		return
	}
	event.Outcome = outcome
	if _, err := h.audit.AppendAudit(ctx, audit, event); err != nil {
		log.Printf("management audit outcome append failed: %v", err)
	}
}
func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
