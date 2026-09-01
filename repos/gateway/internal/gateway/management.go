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
	Alias          string     `json:"alias,omitempty"`
	Description    string     `json:"description,omitempty"`
	Tags           []string   `json:"tags,omitempty"`
	UserID         string     `json:"user_id,omitempty"`
	TeamID         string     `json:"team_id,omitempty"`
	OrganizationID string     `json:"organization_id,omitempty"`
	Roles          []string   `json:"roles,omitempty"`
	AllowedModels  []string   `json:"allowed_models,omitempty"`
	AllowedTools   []string   `json:"allowed_tools,omitempty"`
	RateLimitRPM   int        `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int        `json:"rate_limit_tpm,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
}

type IssuedVirtualKey struct {
	ID        string     `json:"id"`
	Token     string     `json:"token"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type VirtualKeyMetadata struct {
	ID             string     `json:"id"`
	Alias          string     `json:"alias,omitempty"`
	Description    string     `json:"description,omitempty"`
	Tags           []string   `json:"tags,omitempty"`
	UserID         string     `json:"user_id,omitempty"`
	TeamID         string     `json:"team_id,omitempty"`
	OrganizationID string     `json:"organization_id,omitempty"`
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
	DisabledAt     *time.Time `json:"disabled_at,omitempty"`
	LastUsedAt     *time.Time `json:"last_used_at,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
}

type VirtualKeyListFilter struct {
	Limit          int
	Offset         int
	Search         string
	OrganizationID string
	TeamID         string
	UserID         string
	KeyID          string
	Status         string
	SortBy         string
	SortOrder      string
}

type VirtualKeyPage struct {
	Data       []VirtualKeyMetadata           `json:"data"`
	Total      int                            `json:"total"`
	Limit      int                            `json:"limit"`
	Offset     int                            `json:"offset"`
	Financials map[string]KeyBudgetProjection `json:"financials,omitempty"`
}

type ManagementAudit struct {
	RequestID    string
	ActorID      string
	CredentialID string
	TeamID       string
	Roles        []string
}

type ManagementClient interface {
	ListVirtualKeys(context.Context, ManagementAudit, int) ([]VirtualKeyMetadata, error)
	CreateVirtualKey(context.Context, ManagementAudit, ManagedVirtualKey) (IssuedVirtualKey, error)
	RotateVirtualKey(context.Context, ManagementAudit, string, ManagedVirtualKey) (IssuedVirtualKey, error)
	UpdateVirtualKey(context.Context, ManagementAudit, string, ManagedVirtualKey) error
	SetVirtualKeyDisabled(context.Context, ManagementAudit, string, bool) error
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
	page, err := c.ListVirtualKeysPage(ctx, audit, VirtualKeyListFilter{Limit: limit})
	return page.Data, err
}

func (c *RemoteManagementClient) ListVirtualKeysPage(ctx context.Context, audit ManagementAudit, filter VirtualKeyListFilter) (VirtualKeyPage, error) {
	query := url.Values{}
	query.Set("limit", strconv.Itoa(filter.Limit))
	if filter.Offset != 0 {
		query.Set("offset", strconv.Itoa(filter.Offset))
	}
	for key, value := range map[string]string{"search": filter.Search, "organization_id": filter.OrganizationID, "team_id": filter.TeamID, "user_id": filter.UserID, "key_id": filter.KeyID, "status": filter.Status, "sort_by": filter.SortBy, "sort_order": filter.SortOrder} {
		if value != "" {
			query.Set(key, value)
		}
	}
	return managementCall[struct{}, VirtualKeyPage](ctx, c, http.MethodGet, "/internal/v1/keys?"+query.Encode(), audit, struct{}{})
}

func (c *RemoteManagementClient) RotateVirtualKey(ctx context.Context, audit ManagementAudit, id string, spec ManagedVirtualKey) (IssuedVirtualKey, error) {
	return managementCall[ManagedVirtualKey, IssuedVirtualKey](ctx, c, http.MethodPost, "/internal/v1/keys/"+url.PathEscape(id)+"/rotate", audit, spec)
}

func (c *RemoteManagementClient) RevokeVirtualKey(ctx context.Context, audit ManagementAudit, id string) error {
	_, err := managementCall[struct{}, struct{}](ctx, c, http.MethodDelete, "/internal/v1/keys/"+url.PathEscape(id), audit, struct{}{})
	return err
}

func (c *RemoteManagementClient) UpdateVirtualKey(ctx context.Context, audit ManagementAudit, id string, spec ManagedVirtualKey) error {
	_, err := managementCall[ManagedVirtualKey, struct{}](ctx, c, http.MethodPut, "/internal/v1/keys/"+url.PathEscape(id), audit, spec)
	return err
}

func (c *RemoteManagementClient) SetVirtualKeyDisabled(ctx context.Context, audit ManagementAudit, id string, disabled bool) error {
	action := "enable"
	if disabled {
		action = "disable"
	}
	_, err := managementCall[struct{}, struct{}](ctx, c, http.MethodPost, "/internal/v1/keys/"+url.PathEscape(id)+"/"+action, audit, struct{}{})
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
	httpRequest.Header.Set("X-Actor-Team-ID", audit.TeamID)
	httpRequest.Header.Set("X-Actor-Roles", strings.Join(audit.Roles, ","))
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
	filter := VirtualKeyListFilter{Limit: 100}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 500 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 500")
			return
		}
		filter.Limit = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 1_000_000 {
			writeError(w, http.StatusBadRequest, "invalid_request", "offset must be between 0 and 1000000")
			return
		}
		filter.Offset = parsed
	}
	for name, target := range map[string]*string{"search": &filter.Search, "organization_id": &filter.OrganizationID, "team_id": &filter.TeamID, "user_id": &filter.UserID, "key_id": &filter.KeyID, "status": &filter.Status, "sort_by": &filter.SortBy, "sort_order": &filter.SortOrder} {
		*target = strings.TrimSpace(r.URL.Query().Get(name))
	}
	expand := strings.TrimSpace(r.URL.Query().Get("expand"))
	if len(filter.Search) > 128 || len(filter.OrganizationID) > 256 || len(filter.TeamID) > 256 || len(filter.UserID) > 256 || len(filter.KeyID) > 256 || !allowedValue(filter.Status, "", "active", "disabled", "revoked", "expired") || !allowedValue(filter.SortBy, "", "key", "alias", "organization", "team", "user", "created", "status") || !allowedValue(filter.SortOrder, "", "asc", "desc") {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key list filter")
		return
	}
	if !allowedValue(expand, "", "financials") {
		writeError(w, http.StatusBadRequest, "invalid_request", "expand must be financials")
		return
	}
	audit := managementAudit(req)
	pager, supportsPage := h.management.(interface {
		ListVirtualKeysPage(context.Context, ManagementAudit, VirtualKeyListFilter) (VirtualKeyPage, error)
	})
	if supportsPage {
		page, err := pager.ListVirtualKeysPage(r.Context(), audit, filter)
		if err != nil {
			writeManagementFailure(w, err)
			return
		}
		if expand == "financials" && !h.expandKeyFinancials(w, r, audit, &page) {
			return
		}
		writeJSON(w, http.StatusOK, page)
		return
	}
	keys, err := h.management.ListVirtualKeys(r.Context(), audit, filter.Limit)
	if err != nil {
		writeManagementFailure(w, err)
		return
	}
	page := VirtualKeyPage{Data: keys, Total: len(keys), Limit: filter.Limit, Offset: filter.Offset}
	if expand == "financials" && !h.expandKeyFinancials(w, r, audit, &page) {
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h Handler) expandKeyFinancials(w http.ResponseWriter, r *http.Request, audit ManagementAudit, page *VirtualKeyPage) bool {
	if len(page.Data) == 0 {
		page.Financials = map[string]KeyBudgetProjection{}
		return true
	}
	client, ok := h.budgets.(KeyBudgetProjectionClient)
	if !ok || client == nil {
		writeError(w, http.StatusServiceUnavailable, "budget_unavailable", "key budget projection is not configured")
		return false
	}
	subjects := make([]KeyBudgetSubject, 0, len(page.Data))
	for _, key := range page.Data {
		subjects = append(subjects, KeyBudgetSubject{KeyID: key.ID, UserID: key.UserID, TeamID: key.TeamID, OrganizationID: key.OrganizationID})
	}
	projections, err := client.KeyProjections(r.Context(), audit, subjects)
	if err != nil {
		writeBudgetManagementFailure(w, err)
		return false
	}
	page.Financials = make(map[string]KeyBudgetProjection, len(projections))
	for _, projection := range projections {
		page.Financials[projection.KeyID] = projection
	}
	for _, subject := range subjects {
		if _, exists := page.Financials[subject.KeyID]; !exists {
			page.Financials[subject.KeyID] = KeyBudgetProjection{KeyID: subject.KeyID, Policies: []BudgetSummary{}}
		}
	}
	return true
}

func allowedValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
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

func (h Handler) UpdateVirtualKey(w http.ResponseWriter, r *http.Request) {
	h.mutateVirtualKeyPolicy(w, r)
}

func (h Handler) DisableVirtualKey(w http.ResponseWriter, r *http.Request) {
	h.setVirtualKeyDisabled(w, r, true)
}
func (h Handler) EnableVirtualKey(w http.ResponseWriter, r *http.Request) {
	h.setVirtualKeyDisabled(w, r, false)
}

func (h Handler) mutateVirtualKeyPolicy(w http.ResponseWriter, r *http.Request) {
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
	event := AuditEvent{Action: "virtual_key.update", TargetType: "virtual_key", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := h.management.UpdateVirtualKey(r.Context(), audit, id, spec); err != nil {
		h.auditOutcome(r.Context(), audit, event, "failed")
		writeManagementFailure(w, err)
		return
	}
	h.auditOutcome(r.Context(), audit, event, "succeeded")
	w.WriteHeader(http.StatusNoContent)
}

func (h Handler) setVirtualKeyDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
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
	action := "virtual_key.enable"
	if disabled {
		action = "virtual_key.disable"
	}
	audit := managementAudit(req)
	event := AuditEvent{Action: action, TargetType: "virtual_key", TargetID: id}
	if !h.auditMutation(r.Context(), audit, event) {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "audit service is unavailable")
		return
	}
	if err := h.management.SetVirtualKeyDisabled(r.Context(), audit, id, disabled); err != nil {
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
	if (strings.TrimSpace(spec.UserID) == "" && strings.TrimSpace(spec.TeamID) == "" && strings.TrimSpace(spec.OrganizationID) == "") || spec.RateLimitRPM < 0 || spec.RateLimitTPM < 0 || (spec.ExpiresAt != nil && !spec.ExpiresAt.After(time.Now())) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid virtual key policy")
		return spec, false
	}
	return spec, true
}

func managementAudit(req modules.RequestContext) ManagementAudit {
	return ManagementAudit{RequestID: req.RequestID, ActorID: req.UserID, CredentialID: req.CredentialID, TeamID: req.TeamID, Roles: append([]string(nil), req.Roles...)}
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
