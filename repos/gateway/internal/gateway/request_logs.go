package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type RequestLogFilter struct {
	Days            int
	Limit           int
	Before          time.Time
	BeforeRequestID string
	RequestID       string
	Status          string
	Model           string
	Provider        string
	UserID          string
	TeamID          string
	CredentialID    string
}

type RequestLog struct {
	Timestamp            string   `json:"timestamp"`
	RequestID            string   `json:"request_id"`
	UserID               string   `json:"user_id,omitempty"`
	TeamID               string   `json:"team_id,omitempty"`
	Roles                []string `json:"roles,omitempty"`
	CredentialID         string   `json:"credential_id,omitempty"`
	Provider             string   `json:"provider,omitempty"`
	ProviderID           string   `json:"provider_id,omitempty"`
	ProviderEndpointName string   `json:"provider_endpoint_name,omitempty"`
	ProviderEndpointType string   `json:"provider_endpoint_type,omitempty"`
	Model                string   `json:"model,omitempty"`
	UpstreamModel        string   `json:"upstream_model,omitempty"`
	APIType              string   `json:"api_type"`
	Phase                string   `json:"phase"`
	Status               string   `json:"status"`
	FailureClass         string   `json:"failure_class,omitempty"`
	LatencyMS            uint32   `json:"latency_ms"`
	CacheStatus          string   `json:"cache_status,omitempty"`
	InputTokens          uint32   `json:"input_tokens"`
	OutputTokens         uint32   `json:"output_tokens"`
	TotalTokens          uint32   `json:"total_tokens"`
	Cost                 float64  `json:"cost"`
	Currency             string   `json:"currency"`
	ContentStored        bool     `json:"content_stored"`
}

type RequestLogPage struct {
	Data          []RequestLog `json:"data"`
	NextBefore    string       `json:"next_before,omitempty"`
	NextRequestID string       `json:"next_request_id,omitempty"`
}

type RequestLogSettings struct {
	RetentionDays int  `json:"retention_days"`
	ContentStored bool `json:"content_stored"`
	MaxQueryDays  int  `json:"max_query_days"`
}

type RequestLogClient interface {
	ListRequestLogs(context.Context, ManagementAudit, RequestLogFilter) (RequestLogPage, error)
	GetRequestLog(context.Context, ManagementAudit, string) (RequestLog, error)
	GetRequestLogSettings(context.Context, ManagementAudit) (RequestLogSettings, error)
}

func (c *RemoteBudgetManagementClient) ListRequestLogs(ctx context.Context, audit ManagementAudit, filter RequestLogFilter) (RequestLogPage, error) {
	query := url.Values{"days": {strconv.Itoa(filter.Days)}, "limit": {strconv.Itoa(filter.Limit)}}
	for key, value := range map[string]string{"request_id": filter.RequestID, "status": filter.Status, "model": filter.Model, "provider": filter.Provider, "user_id": filter.UserID, "team_id": filter.TeamID, "credential_id": filter.CredentialID} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if !filter.Before.IsZero() {
		query.Set("before", filter.Before.UTC().Format(time.RFC3339Nano))
		query.Set("before_request_id", filter.BeforeRequestID)
	}
	var page RequestLogPage
	err := c.call(ctx, http.MethodGet, "/internal/v1/request-logs?"+query.Encode(), audit, nil, &page)
	return page, err
}

func (c *RemoteBudgetManagementClient) GetRequestLog(ctx context.Context, audit ManagementAudit, requestID string) (RequestLog, error) {
	var row RequestLog
	err := c.call(ctx, http.MethodGet, "/internal/v1/request-logs/"+url.PathEscape(requestID), audit, nil, &row)
	return row, err
}

func (c *RemoteBudgetManagementClient) GetRequestLogSettings(ctx context.Context, audit ManagementAudit) (RequestLogSettings, error) {
	var settings RequestLogSettings
	err := c.call(ctx, http.MethodGet, "/internal/v1/request-logs/settings", audit, nil, &settings)
	return settings, err
}

func (h Handler) WithRequestLogs(client RequestLogClient) Handler {
	h.requestLogs = client
	return h
}

func (h Handler) ListRequestLogs(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.requestLogs == nil {
		writeError(w, http.StatusServiceUnavailable, "request_logs_unavailable", "request logs are not configured")
		return
	}
	filter, err := requestLogFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := h.requestLogs.ListRequestLogs(r.Context(), managementAudit(req), filter)
	if err != nil {
		writeManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h Handler) GetRequestLog(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	requestID := strings.TrimSpace(r.PathValue("request_id"))
	if h.requestLogs == nil {
		writeError(w, http.StatusServiceUnavailable, "request_logs_unavailable", "request logs are not configured")
		return
	}
	if requestID == "" || len(requestID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid request id")
		return
	}
	row, err := h.requestLogs.GetRequestLog(r.Context(), managementAudit(req), requestID)
	if err != nil {
		writeManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, row)
}

func (h Handler) GetRequestLogSettings(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.requestLogs == nil {
		writeError(w, http.StatusServiceUnavailable, "request_logs_unavailable", "request logs are not configured")
		return
	}
	settings, err := h.requestLogs.GetRequestLogSettings(r.Context(), managementAudit(req))
	if err != nil {
		writeManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func requestLogFilter(r *http.Request) (RequestLogFilter, error) {
	days, err := strconv.Atoi(defaultQuery(r, "days", "7"))
	limit, limitErr := strconv.Atoi(defaultQuery(r, "limit", "100"))
	if err != nil || limitErr != nil || days < 1 || days > 90 || limit < 1 || limit > 200 {
		return RequestLogFilter{}, errors.New("days must be 1-90 and limit must be 1-200")
	}
	filter := RequestLogFilter{Days: days, Limit: limit}
	for name, target := range map[string]*string{"request_id": &filter.RequestID, "status": &filter.Status, "model": &filter.Model, "provider": &filter.Provider, "user_id": &filter.UserID, "team_id": &filter.TeamID, "credential_id": &filter.CredentialID} {
		*target = strings.TrimSpace(r.URL.Query().Get(name))
		if len(*target) > 256 {
			return RequestLogFilter{}, errors.New("request log filters must not exceed 256 characters")
		}
	}
	if filter.Status != "" && filter.Status != "ok" && filter.Status != "error" {
		return RequestLogFilter{}, errors.New("status must be ok or error")
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		filter.Before, err = time.Parse(time.RFC3339Nano, raw)
		filter.BeforeRequestID = strings.TrimSpace(r.URL.Query().Get("before_request_id"))
		if err != nil || filter.BeforeRequestID == "" || len(filter.BeforeRequestID) > 256 {
			return RequestLogFilter{}, errors.New("before must be an RFC3339 timestamp")
		}
	}
	return filter, nil
}

func defaultQuery(r *http.Request, name, fallback string) string {
	if value := strings.TrimSpace(r.URL.Query().Get(name)); value != "" {
		return value
	}
	return fallback
}
