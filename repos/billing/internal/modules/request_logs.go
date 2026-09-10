package modules

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const requestLogRetentionDays = 730

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
	InputAudioMilliseconds  uint32   `json:"input_audio_milliseconds"`
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
	GroupID                string   `json:"group_id"`
	Requests               uint64   `json:"requests"`
	Errors                 uint64   `json:"errors"`
	Models                 []string `json:"models"`
	Providers              []string `json:"providers"`
	TotalTokens            uint64   `json:"total_tokens"`
	InputCharacters        uint64   `json:"input_characters"`
	InputPages             uint64   `json:"input_pages"`
	InputAudioMilliseconds uint64   `json:"input_audio_milliseconds"`
	CacheReadInputTokens   uint64   `json:"cache_read_input_tokens"`
	CacheWriteInputTokens  uint64   `json:"cache_write_input_tokens"`
	SearchRequests         uint64   `json:"search_requests"`
	CacheHits              uint64   `json:"cache_hits"`
	LatencyMS              float64  `json:"latency_ms"`
	Cost                   float64  `json:"cost"`
	Currency               string   `json:"currency"`
	StartedAt              string   `json:"started_at"`
	EndedAt                string   `json:"ended_at"`
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

type RequestLogReporter interface {
	ListRequestLogs(context.Context, RequestLogFilter) (RequestLogPage, error)
	ListRequestLogGroups(context.Context, RequestLogGroupFilter) (RequestLogGroupPage, error)
	GetRequestLog(context.Context, string) (RequestLog, error)
	RequestLogSettings() RequestLogSettings
}

func (r *ClickHouseUsageReporter) ListRequestLogGroups(ctx context.Context, filter RequestLogGroupFilter) (RequestLogGroupPage, error) {
	if r == nil || r.client == nil || !validRequestLogFilter(filter.RequestLogFilter) {
		return RequestLogGroupPage{}, errors.New("invalid request log group filter")
	}
	dimension := ""
	switch filter.Dimension {
	case "session":
		dimension = "session_id"
	case "trace":
		dimension = "trace_id"
	default:
		return RequestLogGroupPage{}, errors.New("dimension must be session or trace")
	}
	params, where, err := requestLogQuery(filter.RequestLogFilter, false)
	if err != nil {
		return RequestLogGroupPage{}, err
	}
	groupExpression := fmt.Sprintf("if(%s = '', concat('Unassigned · ', request_id), %s)", dimension, dimension)
	outerWhere := "1"
	if !filter.Before.IsZero() {
		if filter.BeforeGroupID == "" || filter.BeforeCurrency == "" {
			return RequestLogGroupPage{}, errors.New("group id and currency are required with before timestamp")
		}
		outerWhere = "(parseDateTimeBestEffort(ended_at),group_id,currency) < (parseDateTimeBestEffort({before:String}),{before_group_id:String},{before_currency:String})"
		params.Set("param_before", filter.Before.UTC().Format(time.RFC3339Nano))
		params.Set("param_before_group_id", filter.BeforeGroupID)
		params.Set("param_before_currency", filter.BeforeCurrency)
	}
	query := fmt.Sprintf(`SELECT group_id,requests,errors,models,providers,total_tokens,input_characters,input_pages,input_audio_milliseconds,cache_read_input_tokens,cache_write_input_tokens,search_requests,cache_hits,latency_ms,cost,currency,started_at,ended_at
FROM (
 SELECT %s AS group_id,
  count() AS requests,
  countIf(status = 'error') AS errors,
  arraySort(arrayFilter(value -> value != '', groupUniqArray(%s))) AS models,
  arraySort(arrayFilter(value -> value != '', groupUniqArray(%s))) AS providers,
  sum(total_tokens) AS total_tokens,
  sum(input_characters) AS input_characters,
  sum(input_pages) AS input_pages,
	 sum(input_audio_milliseconds) AS input_audio_milliseconds,
  sum(cache_read_input_tokens) AS cache_read_input_tokens,
  sum(cache_write_input_tokens) AS cache_write_input_tokens,
  sum(search_requests) AS search_requests,
  countIf(cache_status = 'hit') AS cache_hits,
  avg(toFloat64(latency_ms)) AS latency_ms,
  sum(usage.cost) AS cost,
  if(currency = '', 'USD', currency) AS currency,
  min(timestamp) AS started_at,
  max(timestamp) AS ended_at
 FROM %s AS usage
 WHERE %s
 GROUP BY group_id,currency
)
WHERE %s
ORDER BY parseDateTimeBestEffort(ended_at) DESC,group_id DESC,currency DESC
LIMIT %d FORMAT JSONEachRow`, groupExpression, canonicalUsageModelExpression, canonicalUsageProviderExpression, r.table, strings.Join(where, " AND "), outerWhere, filter.Limit+1)
	params.Set("query", query)
	rows, err := r.queryRequestLogGroups(ctx, params)
	if err != nil {
		return RequestLogGroupPage{}, err
	}
	page := RequestLogGroupPage{Data: rows}
	if len(rows) > filter.Limit {
		page.Data = rows[:filter.Limit]
		last := page.Data[len(page.Data)-1]
		page.NextBefore = last.EndedAt
		page.NextBeforeGroupID = last.GroupID
		page.NextBeforeCurrency = last.Currency
	}
	return page, nil
}

func (r *ClickHouseUsageReporter) RequestLogSettings() RequestLogSettings {
	return RequestLogSettings{RetentionDays: requestLogRetentionDays, ContentStored: false, MaxQueryDays: 90}
}

func (r *ClickHouseUsageReporter) ListRequestLogs(ctx context.Context, filter RequestLogFilter) (RequestLogPage, error) {
	if r == nil || r.client == nil || !validRequestLogFilter(filter) {
		return RequestLogPage{}, errors.New("invalid request log filter")
	}
	params, where, err := requestLogQuery(filter, true)
	if err != nil {
		return RequestLogPage{}, err
	}
	query := fmt.Sprintf("SELECT %s FROM %s AS usage WHERE %s ORDER BY parseDateTimeBestEffort(timestamp) DESC,request_id DESC LIMIT %d FORMAT JSONEachRow", requestLogColumns(), r.table, strings.Join(where, " AND "), filter.Limit+1)
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

func validRequestLogFilter(filter RequestLogFilter) bool {
	customRange := !filter.From.IsZero() || !filter.To.IsZero()
	if filter.Limit < 1 || filter.Limit > 200 || customRange && (filter.From.IsZero() || filter.To.IsZero() || !filter.To.After(filter.From) || filter.To.Sub(filter.From) > 90*24*time.Hour) || !customRange && (filter.Days < 1 || filter.Days > 90) {
		return false
	}
	for _, value := range []*float64{filter.MinCost, filter.MaxCost} {
		if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
			return false
		}
	}
	return filter.MinCost == nil || filter.MaxCost == nil || *filter.MinCost <= *filter.MaxCost
}

func requestLogQuery(filter RequestLogFilter, includeCursor bool) (url.Values, []string, error) {
	params := url.Values{"output_format_json_quote_64bit_integers": {"0"}}
	where := []string{"phase IN ('commit','cancel')"}
	if !filter.From.IsZero() && !filter.To.IsZero() {
		where = append(where, "timestamp_unix >= {from:UInt64}", "timestamp_unix < {to:UInt64}")
		params.Set("param_from", strconv.FormatInt(filter.From.UTC().Unix(), 10))
		params.Set("param_to", strconv.FormatInt(filter.To.UTC().Unix(), 10))
	} else {
		where = append(where, fmt.Sprintf("timestamp_unix >= toUnixTimestamp(now() - INTERVAL %d DAY)", filter.Days))
	}
	addStringFilter := func(column, name, value string) {
		if value == "" {
			return
		}
		where = append(where, column+" = {"+name+":String}")
		params.Set("param_"+name, value)
	}
	addStringFilter("request_id", "request_id", filter.RequestID)
	addStringFilter("session_id", "session_id", filter.SessionID)
	addStringFilter("trace_id", "trace_id", filter.TraceID)
	addStringFilter("status", "status", filter.Status)
	addStringFilter(canonicalUsageModelExpression, "model", filter.Model)
	addStringFilter(canonicalUsageProviderExpression, "provider", filter.Provider)
	if filter.Tag != "" {
		where = append(where, "has(tags, {tag:String})")
		params.Set("param_tag", filter.Tag)
	}
	addStringFilter("user_id", "user_id", filter.UserID)
	addStringFilter("team_id", "team_id", filter.TeamID)
	addStringFilter("organization_id", "organization_id", filter.OrganizationID)
	addStringFilter("api_key_fingerprint", "credential_id", filter.CredentialID)
	addStringFilter("cache_status", "cache_status", filter.CacheStatus)
	addStringFilter("failure_class", "failure_class", filter.FailureClass)
	if filter.MinCost != nil {
		where = append(where, "usage.cost >= {min_cost:Float64}")
		params.Set("param_min_cost", strconv.FormatFloat(*filter.MinCost, 'g', -1, 64))
	}
	if filter.MaxCost != nil {
		where = append(where, "usage.cost <= {max_cost:Float64}")
		params.Set("param_max_cost", strconv.FormatFloat(*filter.MaxCost, 'g', -1, 64))
	}
	if includeCursor && !filter.Before.IsZero() {
		if filter.BeforeRequestID == "" {
			return nil, nil, errors.New("before request id is required with before timestamp")
		}
		where = append(where, "(parseDateTimeBestEffort(timestamp) < parseDateTimeBestEffort({before:String}) OR (parseDateTimeBestEffort(timestamp) = parseDateTimeBestEffort({before:String}) AND request_id < {before_request_id:String}))")
		params.Set("param_before", filter.Before.UTC().Format(time.RFC3339Nano))
		params.Set("param_before_request_id", filter.BeforeRequestID)
	}
	return params, where, nil
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
	return "timestamp,request_id,session_id,trace_id,user_id,team_id,organization_id,roles,tags,api_key_fingerprint AS credential_id,provider,provider_id,provider_endpoint_name,provider_endpoint_type,model,upstream_model,api_type,phase,status,failure_class,latency_ms,first_token_latency_ms,retry_count,fallback_count,cache_status,cache_kind,input_tokens,output_tokens,total_tokens,input_characters,input_pages,input_audio_milliseconds,cache_read_input_tokens,cache_write_input_tokens,search_requests,search_requests_estimated,usage_estimated,cost,currency,false AS content_stored"
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

func (r *ClickHouseUsageReporter) queryRequestLogGroups(ctx context.Context, params url.Values) ([]RequestLogGroup, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint+"/?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	if r.username != "" || r.password != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query request log groups: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("query request log groups: ClickHouse returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	rows := []RequestLogGroup{}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 8<<20))
	for scanner.Scan() {
		var row RequestLogGroup
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return nil, fmt.Errorf("decode request log group: %w", err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("decode request log groups: %w", err)
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
