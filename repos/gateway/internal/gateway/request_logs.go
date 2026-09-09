package gateway

import (
	"context"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type RequestLogFilter struct {
	Days            int
	From            time.Time
	To              time.Time
	Limit           int
	Before          time.Time
	BeforeRequestID string
	RequestID       string
	SessionID       string
	TraceID         string
	Status          string
	Model           string
	Provider        string
	Tag             string
	UserID          string
	TeamID          string
	OrganizationID  string
	CredentialID    string
	CacheStatus     string
	FailureClass    string
	MinCost         *float64
	MaxCost         *float64
}

type RequestLog struct {
	Timestamp               string   `json:"timestamp"`
	RequestID               string   `json:"request_id"`
	SessionID               string   `json:"session_id,omitempty"`
	TraceID                 string   `json:"trace_id,omitempty"`
	UserID                  string   `json:"user_id,omitempty"`
	TeamID                  string   `json:"team_id,omitempty"`
	OrganizationID          string   `json:"organization_id,omitempty"`
	Roles                   []string `json:"roles,omitempty"`
	Tags                    []string `json:"tags,omitempty"`
	CredentialID            string   `json:"credential_id,omitempty"`
	Provider                string   `json:"provider,omitempty"`
	ProviderID              string   `json:"provider_id,omitempty"`
	ProviderEndpointName    string   `json:"provider_endpoint_name,omitempty"`
	ProviderEndpointType    string   `json:"provider_endpoint_type,omitempty"`
	Model                   string   `json:"model,omitempty"`
	UpstreamModel           string   `json:"upstream_model,omitempty"`
	APIType                 string   `json:"api_type"`
	Phase                   string   `json:"phase"`
	Status                  string   `json:"status"`
	FailureClass            string   `json:"failure_class,omitempty"`
	LatencyMS               uint32   `json:"latency_ms"`
	FirstTokenLatencyMS     uint32   `json:"first_token_latency_ms"`
	RetryCount              uint16   `json:"retry_count"`
	FallbackCount           uint16   `json:"fallback_count"`
	CacheStatus             string   `json:"cache_status,omitempty"`
	CacheKind               string   `json:"cache_kind,omitempty"`
	InputTokens             uint32   `json:"input_tokens"`
	OutputTokens            uint32   `json:"output_tokens"`
	TotalTokens             uint32   `json:"total_tokens"`
	InputCharacters         uint32   `json:"input_characters"`
	InputPages              uint32   `json:"input_pages"`
	CacheReadInputTokens    uint32   `json:"cache_read_input_tokens"`
	CacheWriteInputTokens   uint32   `json:"cache_write_input_tokens"`
	SearchRequests          uint32   `json:"search_requests"`
	SearchRequestsEstimated bool     `json:"search_requests_estimated"`
	UsageEstimated          bool     `json:"usage_estimated"`
	Cost                    float64  `json:"cost"`
	Currency                string   `json:"currency"`
	ContentStored           bool     `json:"content_stored"`
}

type RequestLogPage struct {
	Data          []RequestLog `json:"data"`
	NextBefore    string       `json:"next_before,omitempty"`
	NextRequestID string       `json:"next_request_id,omitempty"`
}

type RequestLogGroupFilter struct {
	RequestLogFilter
	Dimension      string
	BeforeGroupID  string
	BeforeCurrency string
}

type RequestLogGroup struct {
	GroupID               string   `json:"group_id"`
	Requests              uint64   `json:"requests"`
	Errors                uint64   `json:"errors"`
	Models                []string `json:"models"`
	Providers             []string `json:"providers"`
	TotalTokens           uint64   `json:"total_tokens"`
	InputCharacters       uint64   `json:"input_characters"`
	InputPages            uint64   `json:"input_pages"`
	CacheReadInputTokens  uint64   `json:"cache_read_input_tokens"`
	CacheWriteInputTokens uint64   `json:"cache_write_input_tokens"`
	SearchRequests        uint64   `json:"search_requests"`
	CacheHits             uint64   `json:"cache_hits"`
	LatencyMS             float64  `json:"latency_ms"`
	Cost                  float64  `json:"cost"`
	Currency              string   `json:"currency"`
	StartedAt             string   `json:"started_at"`
	EndedAt               string   `json:"ended_at"`
}

type RequestLogGroupPage struct {
	Data               []RequestLogGroup `json:"data"`
	NextBefore         string            `json:"next_before,omitempty"`
	NextBeforeGroupID  string            `json:"next_before_group_id,omitempty"`
	NextBeforeCurrency string            `json:"next_before_currency,omitempty"`
}

type RequestLogSettings struct {
	RetentionDays int  `json:"retention_days"`
	ContentStored bool `json:"content_stored"`
	MaxQueryDays  int  `json:"max_query_days"`
}

type RequestLogClient interface {
	ListRequestLogs(context.Context, ManagementAudit, RequestLogFilter) (RequestLogPage, error)
	ListRequestLogGroups(context.Context, ManagementAudit, RequestLogGroupFilter) (RequestLogGroupPage, error)
	GetRequestLog(context.Context, ManagementAudit, string) (RequestLog, error)
	GetRequestLogSettings(context.Context, ManagementAudit) (RequestLogSettings, error)
}

func (c *RemoteBudgetManagementClient) ListRequestLogGroups(ctx context.Context, audit ManagementAudit, filter RequestLogGroupFilter) (RequestLogGroupPage, error) {
	query := requestLogQuery(filter.RequestLogFilter)
	query.Set("dimension", filter.Dimension)
	if !filter.Before.IsZero() {
		query.Set("before", filter.Before.UTC().Format(time.RFC3339Nano))
		query.Set("before_group_id", filter.BeforeGroupID)
		query.Set("before_currency", filter.BeforeCurrency)
	}
	var page RequestLogGroupPage
	err := c.call(ctx, http.MethodGet, "/internal/v1/request-logs/groups?"+query.Encode(), audit, nil, &page)
	return page, err
}

func (c *RemoteBudgetManagementClient) ListRequestLogs(ctx context.Context, audit ManagementAudit, filter RequestLogFilter) (RequestLogPage, error) {
	query := requestLogQuery(filter)
	if !filter.Before.IsZero() {
		query.Set("before", filter.Before.UTC().Format(time.RFC3339Nano))
		query.Set("before_request_id", filter.BeforeRequestID)
	}
	var page RequestLogPage
	err := c.call(ctx, http.MethodGet, "/internal/v1/request-logs?"+query.Encode(), audit, nil, &page)
	return page, err
}

func requestLogQuery(filter RequestLogFilter) url.Values {
	query := url.Values{"days": {strconv.Itoa(filter.Days)}, "limit": {strconv.Itoa(filter.Limit)}}
	if !filter.From.IsZero() && !filter.To.IsZero() {
		query.Set("from", filter.From.UTC().Format(time.RFC3339))
		query.Set("to", filter.To.UTC().Format(time.RFC3339))
	}
	for key, value := range map[string]string{"request_id": filter.RequestID, "session_id": filter.SessionID, "trace_id": filter.TraceID, "status": filter.Status, "model": filter.Model, "provider": filter.Provider, "tag": filter.Tag, "user_id": filter.UserID, "team_id": filter.TeamID, "organization_id": filter.OrganizationID, "credential_id": filter.CredentialID, "cache_status": filter.CacheStatus, "failure_class": filter.FailureClass} {
		if value != "" {
			query.Set(key, value)
		}
	}
	if filter.MinCost != nil {
		query.Set("min_cost", strconv.FormatFloat(*filter.MinCost, 'g', -1, 64))
	}
	if filter.MaxCost != nil {
		query.Set("max_cost", strconv.FormatFloat(*filter.MaxCost, 'g', -1, 64))
	}
	return query
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

func (h Handler) ListRequestLogGroups(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.requestLogs == nil {
		writeError(w, http.StatusServiceUnavailable, "request_logs_unavailable", "request logs are not configured")
		return
	}
	filter, err := requestLogGroupFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	page, err := h.requestLogs.ListRequestLogGroups(r.Context(), managementAudit(req), filter)
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
	return parseRequestLogFilter(r, true)
}

func requestLogGroupFilter(r *http.Request) (RequestLogGroupFilter, error) {
	filter, err := parseRequestLogFilter(r, false)
	if err != nil {
		return RequestLogGroupFilter{}, err
	}
	dimension := strings.TrimSpace(r.URL.Query().Get("dimension"))
	if dimension != "session" && dimension != "trace" {
		return RequestLogGroupFilter{}, errors.New("dimension must be session or trace")
	}
	groupFilter := RequestLogGroupFilter{RequestLogFilter: filter, Dimension: dimension}
	if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
		groupFilter.Before, err = time.Parse(time.RFC3339Nano, raw)
		groupFilter.BeforeGroupID = strings.TrimSpace(r.URL.Query().Get("before_group_id"))
		groupFilter.BeforeCurrency = strings.TrimSpace(r.URL.Query().Get("before_currency"))
		if err != nil || groupFilter.BeforeGroupID == "" || len(groupFilter.BeforeGroupID) > 256 || groupFilter.BeforeCurrency == "" || len(groupFilter.BeforeCurrency) > 16 {
			return RequestLogGroupFilter{}, errors.New("before requires an RFC3339 timestamp, group id, and currency")
		}
	}
	return groupFilter, nil
}

func parseRequestLogFilter(r *http.Request, includeCursor bool) (RequestLogFilter, error) {
	days, err := strconv.Atoi(defaultQuery(r, "days", "7"))
	limit, limitErr := strconv.Atoi(defaultQuery(r, "limit", "100"))
	if err != nil || limitErr != nil || days < 1 || days > 90 || limit < 1 || limit > 200 {
		return RequestLogFilter{}, errors.New("days must be 1-90 and limit must be 1-200")
	}
	filter := RequestLogFilter{Days: days, Limit: limit}
	fromRaw, toRaw := strings.TrimSpace(r.URL.Query().Get("from")), strings.TrimSpace(r.URL.Query().Get("to"))
	if (fromRaw == "") != (toRaw == "") {
		return RequestLogFilter{}, errors.New("from and to must be provided together")
	}
	if fromRaw != "" {
		filter.From, err = time.Parse(time.RFC3339, fromRaw)
		if err == nil {
			filter.To, err = time.Parse(time.RFC3339, toRaw)
		}
		if err != nil || !filter.To.After(filter.From) || filter.To.Sub(filter.From) > 90*24*time.Hour {
			return RequestLogFilter{}, errors.New("from/to must be an RFC3339 range of at most 90 days")
		}
	}
	for name, target := range map[string]*string{"request_id": &filter.RequestID, "session_id": &filter.SessionID, "trace_id": &filter.TraceID, "status": &filter.Status, "model": &filter.Model, "provider": &filter.Provider, "tag": &filter.Tag, "user_id": &filter.UserID, "team_id": &filter.TeamID, "organization_id": &filter.OrganizationID, "credential_id": &filter.CredentialID, "cache_status": &filter.CacheStatus, "failure_class": &filter.FailureClass} {
		*target = strings.TrimSpace(r.URL.Query().Get(name))
		if len(*target) > 256 {
			return RequestLogFilter{}, errors.New("request log filters must not exceed 256 characters")
		}
	}
	if filter.Status != "" && filter.Status != "ok" && filter.Status != "error" {
		return RequestLogFilter{}, errors.New("status must be ok or error")
	}
	if filter.CacheStatus != "" && filter.CacheStatus != "hit" && filter.CacheStatus != "miss" && filter.CacheStatus != "error" {
		return RequestLogFilter{}, errors.New("cache status must be hit, miss, or error")
	}
	for name, target := range map[string]**float64{"min_cost": &filter.MinCost, "max_cost": &filter.MaxCost} {
		raw := strings.TrimSpace(r.URL.Query().Get(name))
		if raw == "" {
			continue
		}
		value, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return RequestLogFilter{}, errors.New("cost filters must be finite non-negative numbers")
		}
		*target = &value
	}
	if filter.MinCost != nil && filter.MaxCost != nil && *filter.MinCost > *filter.MaxCost {
		return RequestLogFilter{}, errors.New("min cost must not exceed max cost")
	}
	if filter.TraceID != "" {
		if len(filter.TraceID) != 32 {
			return RequestLogFilter{}, errors.New("trace id must be 32 hexadecimal characters")
		}
		if _, err := hex.DecodeString(filter.TraceID); err != nil {
			return RequestLogFilter{}, errors.New("trace id must be 32 hexadecimal characters")
		}
	}
	if includeCursor {
		if raw := strings.TrimSpace(r.URL.Query().Get("before")); raw != "" {
			filter.Before, err = time.Parse(time.RFC3339Nano, raw)
			filter.BeforeRequestID = strings.TrimSpace(r.URL.Query().Get("before_request_id"))
			if err != nil || filter.BeforeRequestID == "" || len(filter.BeforeRequestID) > 256 {
				return RequestLogFilter{}, errors.New("before must be an RFC3339 timestamp")
			}
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
