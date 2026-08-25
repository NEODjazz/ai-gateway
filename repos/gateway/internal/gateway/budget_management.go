package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type BudgetPolicySpec struct {
	ScopeType string   `json:"scope_type"`
	ScopeID   string   `json:"scope_id"`
	Period    string   `json:"period"`
	Currency  string   `json:"currency"`
	MaxCost   *float64 `json:"max_cost,omitempty"`
	MaxTokens *int64   `json:"max_tokens,omitempty"`
	Enabled   *bool    `json:"enabled,omitempty"`
}

type ManagedBudgetPolicy struct {
	ID        int64     `json:"id"`
	ScopeType string    `json:"scope_type"`
	ScopeID   string    `json:"scope_id"`
	Period    string    `json:"period"`
	Currency  string    `json:"currency"`
	MaxCost   *float64  `json:"max_cost,omitempty"`
	MaxTokens *int64    `json:"max_tokens,omitempty"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type BudgetSummary struct {
	Policy          ManagedBudgetPolicy `json:"policy"`
	WindowStart     time.Time           `json:"window_start"`
	WindowEnd       time.Time           `json:"window_end"`
	UsedCost        float64             `json:"used_cost"`
	RemainingCost   *float64            `json:"remaining_cost,omitempty"`
	UsedTokens      int64               `json:"used_tokens"`
	RemainingTokens *int64              `json:"remaining_tokens,omitempty"`
}

type BudgetManagementClient interface {
	List(context.Context, ManagementAudit) ([]ManagedBudgetPolicy, error)
	Get(context.Context, ManagementAudit, int64) (ManagedBudgetPolicy, error)
	Create(context.Context, ManagementAudit, BudgetPolicySpec) (ManagedBudgetPolicy, error)
	Update(context.Context, ManagementAudit, int64, BudgetPolicySpec) (ManagedBudgetPolicy, error)
	Disable(context.Context, ManagementAudit, int64) error
	Summary(context.Context, ManagementAudit, int64) (BudgetSummary, error)
}

type RemoteBudgetManagementClient struct {
	baseURL, secret string
	client          *http.Client
}

func NewRemoteBudgetManagementClient(baseURL, secret string) *RemoteBudgetManagementClient {
	return &RemoteBudgetManagementClient{baseURL: strings.TrimRight(baseURL, "/"), secret: secret, client: &http.Client{Timeout: 5 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport)}}
}

func (c *RemoteBudgetManagementClient) List(ctx context.Context, audit ManagementAudit) ([]ManagedBudgetPolicy, error) {
	var response struct {
		Data []ManagedBudgetPolicy `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, "/internal/v1/budgets", audit, nil, &response)
	return response.Data, err
}
func (c *RemoteBudgetManagementClient) Get(ctx context.Context, audit ManagementAudit, id int64) (ManagedBudgetPolicy, error) {
	var v ManagedBudgetPolicy
	err := c.call(ctx, http.MethodGet, budgetPath(id), audit, nil, &v)
	return v, err
}
func (c *RemoteBudgetManagementClient) Create(ctx context.Context, audit ManagementAudit, spec BudgetPolicySpec) (ManagedBudgetPolicy, error) {
	var v ManagedBudgetPolicy
	err := c.call(ctx, http.MethodPost, "/internal/v1/budgets", audit, spec, &v)
	return v, err
}
func (c *RemoteBudgetManagementClient) Update(ctx context.Context, audit ManagementAudit, id int64, spec BudgetPolicySpec) (ManagedBudgetPolicy, error) {
	var v ManagedBudgetPolicy
	err := c.call(ctx, http.MethodPut, budgetPath(id), audit, spec, &v)
	return v, err
}
func (c *RemoteBudgetManagementClient) Disable(ctx context.Context, audit ManagementAudit, id int64) error {
	return c.call(ctx, http.MethodDelete, budgetPath(id), audit, nil, nil)
}
func (c *RemoteBudgetManagementClient) Summary(ctx context.Context, audit ManagementAudit, id int64) (BudgetSummary, error) {
	var v BudgetSummary
	err := c.call(ctx, http.MethodGet, budgetPath(id)+"/summary", audit, nil, &v)
	return v, err
}
func budgetPath(id int64) string {
	return "/internal/v1/budgets/" + url.PathEscape(strconv.FormatInt(id, 10))
}

func (c *RemoteBudgetManagementClient) call(ctx context.Context, method, path string, audit ManagementAudit, payload, result any) error {
	if c == nil || c.baseURL == "" || c.secret == "" {
		return errors.New("budget management service is not configured")
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Management-Token", c.secret)
	req.Header.Set("X-Request-ID", audit.RequestID)
	req.Header.Set("X-Actor-ID", audit.ActorID)
	req.Header.Set("X-Actor-Credential-ID", audit.CredentialID)
	response, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &ManagementError{Status: response.StatusCode}
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(result)
}

func (h Handler) WithBudgetManagement(client BudgetManagementClient) Handler {
	h.budgets = client
	return h
}

func (h Handler) budgetAdmin(w http.ResponseWriter, r *http.Request) (ManagementAudit, bool) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return ManagementAudit{}, false
	}
	if h.budgets == nil {
		writeError(w, http.StatusServiceUnavailable, "management_unavailable", "budget management service is not configured")
		return ManagementAudit{}, false
	}
	return managementAudit(req), true
}
func (h Handler) ListBudgets(w http.ResponseWriter, r *http.Request) {
	audit, ok := h.budgetAdmin(w, r)
	if !ok {
		return
	}
	v, err := h.budgets.List(r.Context(), audit)
	if err != nil {
		writeBudgetManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": v})
}
func (h Handler) GetBudget(w http.ResponseWriter, r *http.Request) {
	audit, id, ok := h.budgetRequest(w, r)
	if !ok {
		return
	}
	v, err := h.budgets.Get(r.Context(), audit, id)
	if err != nil {
		writeBudgetManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h Handler) CreateBudget(w http.ResponseWriter, r *http.Request) {
	audit, ok := h.budgetAdmin(w, r)
	if !ok {
		return
	}
	spec, ok := decodeBudgetPolicy(w, r)
	if !ok {
		return
	}
	v, err := h.budgets.Create(r.Context(), audit, spec)
	if err != nil {
		writeBudgetManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}
func (h Handler) UpdateBudget(w http.ResponseWriter, r *http.Request) {
	audit, id, ok := h.budgetRequest(w, r)
	if !ok {
		return
	}
	spec, ok := decodeBudgetPolicy(w, r)
	if !ok {
		return
	}
	v, err := h.budgets.Update(r.Context(), audit, id, spec)
	if err != nil {
		writeBudgetManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h Handler) DisableBudget(w http.ResponseWriter, r *http.Request) {
	audit, id, ok := h.budgetRequest(w, r)
	if !ok {
		return
	}
	if err := h.budgets.Disable(r.Context(), audit, id); err != nil {
		writeBudgetManagementFailure(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h Handler) GetBudgetSummary(w http.ResponseWriter, r *http.Request) {
	audit, id, ok := h.budgetRequest(w, r)
	if !ok {
		return
	}
	v, err := h.budgets.Summary(r.Context(), audit, id)
	if err != nil {
		writeBudgetManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h Handler) budgetRequest(w http.ResponseWriter, r *http.Request) (ManagementAudit, int64, bool) {
	audit, ok := h.budgetAdmin(w, r)
	if !ok {
		return ManagementAudit{}, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid budget id")
		return ManagementAudit{}, 0, false
	}
	return audit, id, true
}
func decodeBudgetPolicy(w http.ResponseWriter, r *http.Request) (BudgetPolicySpec, bool) {
	var spec BudgetPolicySpec
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&spec) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid budget policy")
		return spec, false
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid budget policy")
		return spec, false
	}
	return spec, true
}

func writeBudgetManagementFailure(w http.ResponseWriter, err error) {
	var managementErr *ManagementError
	if errors.As(err, &managementErr) {
		switch managementErr.Status {
		case http.StatusBadRequest:
			writeError(w, http.StatusBadRequest, "invalid_request", "invalid budget policy")
			return
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "not_found", "budget policy not found")
			return
		}
	}
	writeError(w, http.StatusServiceUnavailable, "management_unavailable", "budget management service is unavailable")
}
