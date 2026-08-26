package modules

import (
	"bufio"
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
)

const requestLogRetentionDays = 730

type RequestLogFilter struct {
	Days            int
	Limit           int
	Before          time.Time
	BeforeRequestID string
	RequestID       string
	SessionID       string
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
	SessionID            string   `json:"session_id,omitempty"`
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

type RequestLogReporter interface {
	ListRequestLogs(context.Context, RequestLogFilter) (RequestLogPage, error)
	GetRequestLog(context.Context, string) (RequestLog, error)
	RequestLogSettings() RequestLogSettings
}

func (r *ClickHouseUsageReporter) RequestLogSettings() RequestLogSettings {
	return RequestLogSettings{RetentionDays: requestLogRetentionDays, ContentStored: false, MaxQueryDays: 90}
}

func (r *ClickHouseUsageReporter) ListRequestLogs(ctx context.Context, filter RequestLogFilter) (RequestLogPage, error) {
	if r == nil || r.client == nil || filter.Days < 1 || filter.Days > 90 || filter.Limit < 1 || filter.Limit > 200 {
		return RequestLogPage{}, errors.New("invalid request log filter")
	}
	params := url.Values{"output_format_json_quote_64bit_integers": {"0"}}
	where := []string{fmt.Sprintf("timestamp_unix >= toUnixTimestamp(now() - INTERVAL %d DAY)", filter.Days), "phase IN ('commit','cancel')"}
	addStringFilter := func(column, name, value string) {
		if value == "" {
			return
		}
		where = append(where, column+" = {"+name+":String}")
		params.Set("param_"+name, value)
	}
	addStringFilter("request_id", "request_id", filter.RequestID)
	addStringFilter("session_id", "session_id", filter.SessionID)
	addStringFilter("status", "status", filter.Status)
	addStringFilter(canonicalUsageModelExpression, "model", filter.Model)
	addStringFilter(canonicalUsageProviderExpression, "provider", filter.Provider)
	addStringFilter("user_id", "user_id", filter.UserID)
	addStringFilter("team_id", "team_id", filter.TeamID)
	addStringFilter("api_key_fingerprint", "credential_id", filter.CredentialID)
	if !filter.Before.IsZero() {
		if filter.BeforeRequestID == "" {
			return RequestLogPage{}, errors.New("before request id is required with before timestamp")
		}
		where = append(where, "(parseDateTimeBestEffort(timestamp) < parseDateTimeBestEffort({before:String}) OR (parseDateTimeBestEffort(timestamp) = parseDateTimeBestEffort({before:String}) AND request_id < {before_request_id:String}))")
		params.Set("param_before", filter.Before.UTC().Format(time.RFC3339Nano))
		params.Set("param_before_request_id", filter.BeforeRequestID)
	}
	query := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY parseDateTimeBestEffort(timestamp) DESC,request_id DESC LIMIT %d FORMAT JSONEachRow", requestLogColumns(), r.table, strings.Join(where, " AND "), filter.Limit+1)
	params.Set("query", query)
	rows, err := r.queryRequestLogs(ctx, params)
	if err != nil {
		return RequestLogPage{}, err
	}
	page := RequestLogPage{Data: rows}
	if len(rows) > filter.Limit {
		page.Data = rows[:filter.Limit]
		page.NextBefore = page.Data[len(page.Data)-1].Timestamp
		page.NextRequestID = page.Data[len(page.Data)-1].RequestID
	}
	return page, nil
}

func (r *ClickHouseUsageReporter) GetRequestLog(ctx context.Context, requestID string) (RequestLog, error) {
	if r == nil || r.client == nil || strings.TrimSpace(requestID) == "" {
		return RequestLog{}, errors.New("request id is required")
	}
	params := url.Values{
		"output_format_json_quote_64bit_integers": {"0"},
		"param_request_id":                        {requestID},
		"query":                                   {fmt.Sprintf("SELECT %s FROM %s WHERE request_id = {request_id:String} AND phase IN ('commit','cancel') ORDER BY parseDateTimeBestEffort(timestamp) DESC,event_id DESC LIMIT 1 FORMAT JSONEachRow", requestLogColumns(), r.table)},
	}
	rows, err := r.queryRequestLogs(ctx, params)
	if err != nil {
		return RequestLog{}, err
	}
	if len(rows) == 0 {
		return RequestLog{}, ErrRequestLogNotFound
	}
	return rows[0], nil
}

var ErrRequestLogNotFound = errors.New("request log not found")

func requestLogColumns() string {
	return "timestamp,request_id,session_id,user_id,team_id,roles,api_key_fingerprint AS credential_id,provider,provider_id,provider_endpoint_name,provider_endpoint_type,model,upstream_model,api_type,phase,status,failure_class,latency_ms,cache_status,input_tokens,output_tokens,total_tokens,cost,currency,false AS content_stored"
}

func (r *ClickHouseUsageReporter) queryRequestLogs(ctx context.Context, params url.Values) ([]RequestLog, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint+"/?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if r.username != "" || r.password != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query request logs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("query request logs: ClickHouse returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	rows := []RequestLog{}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	for scanner.Scan() {
		var row RequestLog
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("decode request log: %w", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("decode request logs: %w", err)
	}
	return rows, nil
}

func ParseRequestLogLimit(raw string, fallback int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 200 {
		return 0, errors.New("limit must be between 1 and 200")
	}
	return value, nil
}
