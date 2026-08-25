package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/modules"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type ManagedVirtualKey struct {
	UserID        string     `json:"user_id"`
	TeamID        string     `json:"team_id,omitempty"`
	Roles         []string   `json:"roles,omitempty"`
	AllowedModels []string   `json:"allowed_models,omitempty"`
	AllowedTools  []string   `json:"allowed_tools,omitempty"`
	RateLimitRPM  int        `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM  int        `json:"rate_limit_tpm,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

type IssuedVirtualKey struct {
	ID        string     `json:"id"`
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type VirtualKeyMetadata struct {
	ID             string     `json:"id"`
	UserID         string     `json:"user_id"`
	TeamID         string     `json:"team_id,omitempty"`
	Roles          []string   `json:"roles,omitempty"`
	AllowedModels  []string   `json:"allowed_models,omitempty"`
	AllowedTools   []string   `json:"allowed_tools,omitempty"`
	RateLimitRPM   int        `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int        `json:"rate_limit_tpm,omitempty"`
	RotationFamily string     `json:"rotation_family_id"`
	RotatedFromID  string     `json:"rotated_from_id,omitempty"`
	RotatedToID    string     `json:"rotated_to_id,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type ManagementAudit struct {
	RequestID    string
	ActorID      string
	CredentialID string
}

type ManagementClient interface {
	ListVirtualKeys(context.Context, ManagementAudit, int) ([]VirtualKeyMetadata, error)
	CreateVirtualKey(context.Context, ManagementAudit, ManagedVirtualKey) (IssuedVirtualKey, error)
	RotateVirtualKey(context.Context, ManagementAudit, string, ManagedVirtualKey) (IssuedVirtualKey, error)
	RevokeVirtualKey(context.Context, ManagementAudit, string) error
}

type RemoteManagementClient struct {
	baseURL string
	secret  string
	client  *http.Client
}

type ManagementError struct {
	Status int
}

func (e *ManagementError) Error() string {
	return fmt.Sprintf("management service returned status %d", e.Status)
}

func NewRemoteManagementClient(baseURL, secret string) *RemoteManagementClient {
	return &RemoteManagementClient{
		baseURL: strings.TrimRight(baseURL, "/"), secret: secret,
		client: &http.Client{Timeout: 5 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)},
	}
}

func (c *RemoteManagementClient) CreateVirtualKey(ctx context.Context, audit ManagementAudit, spec ManagedVirtualKey) (IssuedVirtualKey, error) {
	return managementCall[ManagedVirtualKey, IssuedVirtualKey](ctx, c, http.MethodPost, "/internal/v1/keys", audit, spec)
}

func (c *RemoteManagementClient) ListVirtualKeys(ctx context.Context, audit ManagementAudit, limit int) ([]VirtualKeyMetadata, error) {
	result, err := managementCall[struct{}, struct {
		Data []VirtualKeyMetadata `json:"data"`
	}](ctx, c, http.MethodGet, "/internal/v1/keys?limit="+url.QueryEscape(fmt.Sprint(limit)), audit, struct{}{})
	return result.Data, err
}

func (c *RemoteManagementClient) RotateVirtualKey(ctx context.Context, audit ManagementAudit, id string, spec ManagedVirtualKey) (IssuedVirtualKey, error) {
	return managementCall[ManagedVirtualKey, IssuedVirtualKey](ctx, c, http.MethodPost, "/internal/v1/keys/"+url.PathEscape(id)+"/rotate", audit, spec)
}

func (c *RemoteManagementClient) RevokeVirtualKey(ctx context.Context, audit ManagementAudit, id string) error {
	_, err := managementCall[struct{}, struct{}](ctx, c, http.MethodDelete, "/internal/v1/keys/"+url.PathEscape(id), audit, struct{}{})
	return err
}

func managementCall[Request any, Response any](ctx context.Context, client *RemoteManagementClient, method, path string, audit ManagementAudit, request Request) (Response, error) {
	var result Response
	if client == nil || client.baseURL == "" || client.secret == "" {
		return result, errors.New("management service is not configured")
	}
	var body io.Reader
	if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
		payload, err := json.Marshal(request)
		if err != nil {
			return result, err
		}
		body = bytes.NewReader(payload)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, body)
	if err != nil {
		return result, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("X-Management-Token", client.secret)
	httpRequest.Header.Set("X-Request-ID", audit.RequestID)
	httpRequest.Header.Set("X-Actor-ID", audit.ActorID)
	httpRequest.Header.Set("X-Actor-Credential-ID", audit.CredentialID)
	response, err := client.client.Do(httpRequest)
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return result, &ManagementError{Status: response.StatusCode}
	}
	if response.StatusCode == http.StatusNoContent {
		return result, nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil {
		return result, err
	}
	return result, nil
}

func (h Handler) ListVirtualKeys(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.management == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "management service is not configured")
		return
	}
	limit := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 500")
			return
		}
		limit = parsed
	}
	keys, err := h.management.ListVirtualKeys(r.Context(), managementAudit(req), limit)
	if err != nil {
		writeManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": keys})
}

func (h Handler) WithManagement(client ManagementClient) Handler {
	h.management = client
	return h
}

func (h Handler) CreateVirtualKey(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.management == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "management service is not configured")
		return
	}
	spec, ok := decodeManagedVirtualKey(w, r)
	if !ok {
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "virtual_key.create", TargetType: "virtual_key"}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	issued, err := h.management.CreateVirtualKey(r.Context(), audit, spec)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeManagementFailure(w, err)
		return
	}
	event.TargetID = issued.ID
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusCreated, issued)
}

func (h Handler) RotateVirtualKey(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.management == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "management service is not configured")
		return
	}
	id := r.PathValue("id")
	if !validVirtualKeyID(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key id")
		return
	}
	spec, ok := decodeManagedVirtualKey(w, r)
	if !ok {
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "virtual_key.rotate", TargetType: "virtual_key", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	issued, err := h.management.RotateVirtualKey(r.Context(), audit, id, spec)
	if err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeManagementFailure(w, err)
		return
	}
	event.TargetID = issued.ID
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	writeJSON(w, http.StatusCreated, issued)
}

func (h Handler) RevokeVirtualKey(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.management == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "management service is not configured")
		return
	}
	id := r.PathValue("id")
	if !validVirtualKeyID(id) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key id")
		return
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: "virtual_key.revoke", TargetType: "virtual_key", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := h.management.RevokeVirtualKey(r.Context(), audit, id); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeManagementFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) authorizeAdmin(w http.ResponseWriter, r *http.Request) (modules.RequestContext, bool) {
	req := modules.RequestContext{APIKey: bearerToken(r.Header.Get("Authorization")), RequestID: requestID(r)}
	if err := h.pipeline.Run(r.Context(), &req); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
		} else {
			writeError(w, http.StatusBadGateway, "module_failed", err.Error())
		}
		return req, false
	}
	req.APIKey = ""
	if !hasRole(req.Roles, "admin") {
		writeError(w, http.StatusForbidden, "forbidden", "admin role is required")
		return req, false
	}
	return req, true
}

func decodeManagedVirtualKey(w http.ResponseWriter, r *http.Request) (ManagedVirtualKey, bool) {
	var spec ManagedVirtualKey
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&spec); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key policy")
		return spec, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key policy")
		return spec, false
	}
	if spec.UserID == "" || spec.RateLimitRPM < 0 || spec.RateLimitTPM < 0 || (spec.ExpiresAt != nil && !spec.ExpiresAt.After(time.Now())) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key policy")
		return spec, false
	}
	return spec, true
}

func managementAudit(req modules.RequestContext) ManagementAudit {
	return ManagementAudit{RequestID: req.RequestID, ActorID: req.UserID, CredentialID: req.CredentialID}
}

func hasRole(roles []string, expected string) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}

func validVirtualKeyID(id string) bool {
	if !strings.HasPrefix(id, "vk_") || len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, value := range id {
		if (value < 'a' || value > 'z') && (value < 'A' || value > 'Z') && (value < '0' || value > '9') && value != '_' && value != '-' {
			return false
		}
	}
	return true
}

func writeManagementFailure(w http.ResponseWriter, err error) {
	var managementErr *ManagementError
	if errors.As(err, &managementErr) {
		switch managementErr.Status {
		case http.StatusBadRequest:
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key policy")
			return
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "not_found", "virtual key not found")
			return
		}
	}
	writeError(w, http.StatusServiceUnavailable, "management_unavailable", "management service is unavailable")
}
