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
	"regexp"
	"strconv"
	"strings"
	"time"
)

type UsageAggregate struct {
	Date           string  `json:"date,omitempty"`
	Name           string  `json:"name,omitempty"`
	Currency       string  `json:"currency"`
	Requests       uint64  `json:"requests"`
	Errors         uint64  `json:"errors"`
	InputTokens    uint64  `json:"input_tokens"`
	OutputTokens   uint64  `json:"output_tokens"`
	TotalTokens    uint64  `json:"total_tokens"`
	Cost           float64 `json:"cost"`
	AvgLatencyMS   float64 `json:"avg_latency_ms"`
	CacheHits      uint64  `json:"cache_hits"`
	CostPerRequest float64 `json:"cost_per_request"`
}

type UsageReport struct {
	Days            int              `json:"days"`
	From            time.Time        `json:"from"`
	To              time.Time        `json:"to"`
	Totals          []UsageAggregate `json:"totals"`
	Daily           []UsageAggregate `json:"daily"`
	ByModel         []UsageAggregate `json:"by_model"`
	ByUpstreamModel []UsageAggregate `json:"by_upstream_model"`
	ByProvider      []UsageAggregate `json:"by_provider"`
	ByEndpoint      []UsageAggregate `json:"by_endpoint"`
	ByTag           []UsageAggregate `json:"by_tag"`
	ByKey           []UsageAggregate `json:"by_key"`
	ByUser          []UsageAggregate `json:"by_user"`
	ByTeam          []UsageAggregate `json:"by_team"`
	ByOrganization  []UsageAggregate `json:"by_organization"`
}

type UsageReporter interface {
	Report(context.Context, int) (UsageReport, error)
}

type UsageScope struct {
	Type string
	ID   string
}

type UsageReportQuery struct {
	From          time.Time
	To            time.Time
	Scope         UsageScope
	Model         string
	UpstreamModel string
	Provider      string
	Endpoint      string
	Tag           string
}

type FilteredUsageReporter interface {
	ReportQuery(context.Context, UsageReportQuery) (UsageReport, error)
}

type ScopedUsageReporter interface {
	ReportScoped(context.Context, int, UsageScope) (UsageReport, error)
}

type ClickHouseUsageReporter struct {
	endpoint string
	table    string
	username string
	password string
	client   *http.Client
	now      func() time.Time
}

var clickHouseIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

const canonicalUsageModelExpression = "multiIf(pricing_key != '' AND position(pricing_key, '/') > 0 AND substring(pricing_key, position(pricing_key, '/') + 1) != '*', substring(pricing_key, position(pricing_key, '/') + 1), provider_endpoint_name != '' AND startsWith(model, concat(provider_endpoint_name, '-')), provider_endpoint_name, model)"
const canonicalUsageProviderExpression = "multiIf(provider_id != '', provider_id, pricing_key != '' AND position(pricing_key, '/') > 0 AND substring(pricing_key, 1, position(pricing_key, '/') - 1) != '*', substring(pricing_key, 1, position(pricing_key, '/') - 1), provider_endpoint_name != '', provider_endpoint_name, provider)"

func NewClickHouseUsageReporter(settings Settings) (*ClickHouseUsageReporter, error) {
	if !settings.UsageEventsEnabled {
		return nil, errors.New("usage reporting requires usage events")
	}
	if !clickHouseIdentifier.MatchString(settings.ClickHouseDatabase) || !clickHouseIdentifier.MatchString(settings.ClickHouseUsageEventsTable) {
		return nil, errors.New("invalid ClickHouse usage table configuration")
	}
	return &ClickHouseUsageReporter{
		endpoint: strings.TrimRight(settings.ClickHouseURL, "/"), table: settings.ClickHouseDatabase + "." + settings.ClickHouseUsageEventsTable, username: settings.ClickHouseUsername,
		password: settings.ClickHousePassword, client: &http.Client{Timeout: 8 * time.Second}, now: time.Now,
	}, nil
}

func (r *ClickHouseUsageReporter) Report(ctx context.Context, days int) (UsageReport, error) {
	to := r.now().UTC()
	return r.ReportQuery(ctx, UsageReportQuery{From: to.AddDate(0, 0, -days), To: to})
}

func (r *ClickHouseUsageReporter) ReportScoped(ctx context.Context, days int, scope UsageScope) (UsageReport, error) {
	if (scope.Type != "key" && scope.Type != "user" && scope.Type != "team" && scope.Type != "organization") || strings.TrimSpace(scope.ID) == "" || len(scope.ID) > 256 {
		return UsageReport{}, errors.New("invalid usage scope")
	}
	to := r.now().UTC()
	return r.ReportQuery(ctx, UsageReportQuery{From: to.AddDate(0, 0, -days), To: to, Scope: scope})
}

func (r *ClickHouseUsageReporter) ReportQuery(ctx context.Context, query UsageReportQuery) (UsageReport, error) {
	if r == nil || r.client == nil || !query.To.After(query.From) || query.To.Sub(query.From) > 90*24*time.Hour || len(query.Model) > 256 || len(query.UpstreamModel) > 256 || len(query.Provider) > 256 || len(query.Endpoint) > 256 || len(query.Tag) > 256 {
		return UsageReport{}, errors.New("invalid usage report query")
	}
	if query.Scope.Type != "" && ((query.Scope.Type != "key" && query.Scope.Type != "user" && query.Scope.Type != "team" && query.Scope.Type != "organization") || strings.TrimSpace(query.Scope.ID) == "" || len(query.Scope.ID) > 256) {
		return UsageReport{}, errors.New("invalid usage scope")
	}
	days := int(query.To.Sub(query.From).Hours()/24 + 0.999999)
	report := UsageReport{Days: days, From: query.From.UTC(), To: query.To.UTC(), Totals: []UsageAggregate{}, Daily: []UsageAggregate{}, ByModel: []UsageAggregate{}, ByUpstreamModel: []UsageAggregate{}, ByProvider: []UsageAggregate{}, ByEndpoint: []UsageAggregate{}, ByTag: []UsageAggregate{}, ByKey: []UsageAggregate{}, ByUser: []UsageAggregate{}, ByTeam: []UsageAggregate{}, ByOrganization: []UsageAggregate{}}
	statement := usageReportQuery(r.table, query.Scope.Type, query.Model != "", query.UpstreamModel != "", query.Provider != "", query.Endpoint != "", query.Tag != "")
	parameters := url.Values{"output_format_json_quote_64bit_integers": {"0"}, "query": {statement}, "param_from": {strconv.FormatInt(query.From.UTC().Unix(), 10)}, "param_to": {strconv.FormatInt(query.To.UTC().Unix(), 10)}}
	if query.Scope.Type != "" {
		parameters.Set("param_scope_id", query.Scope.ID)
	}
	if query.Model != "" {
		parameters.Set("param_model", query.Model)
	}
	if query.UpstreamModel != "" {
		parameters.Set("param_upstream_model", query.UpstreamModel)
	}
	if query.Provider != "" {
		parameters.Set("param_provider", query.Provider)
	}
	if query.Endpoint != "" {
		parameters.Set("param_endpoint", query.Endpoint)
	}
	if query.Tag != "" {
		parameters.Set("param_tag", query.Tag)
	}
	requestURL := r.endpoint + "/?" + parameters.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, nil)
	if err != nil {
		return UsageReport{}, err
	}
	if r.username != "" || r.password != "" {
		req.SetBasicAuth(r.username, r.password)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return UsageReport{}, fmt.Errorf("query usage report: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return UsageReport{}, fmt.Errorf("query usage report: ClickHouse returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	scanner := bufio.NewScanner(io.LimitReader(resp.Body, 2<<20))
	for scanner.Scan() {
		var row struct {
			Kind string `json:"kind"`
			UsageAggregate
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			return UsageReport{}, fmt.Errorf("decode usage report: %w", err)
		}
		switch row.Kind {
		case "total":
			report.Totals = append(report.Totals, row.UsageAggregate)
		case "day":
			report.Daily = append(report.Daily, row.UsageAggregate)
		case "model":
			report.ByModel = append(report.ByModel, row.UsageAggregate)
		case "upstream_model":
			report.ByUpstreamModel = append(report.ByUpstreamModel, row.UsageAggregate)
		case "provider":
			report.ByProvider = append(report.ByProvider, row.UsageAggregate)
		case "endpoint":
			report.ByEndpoint = append(report.ByEndpoint, row.UsageAggregate)
		case "tag":
			report.ByTag = append(report.ByTag, row.UsageAggregate)
		case "key":
			report.ByKey = append(report.ByKey, row.UsageAggregate)
		case "user":
			report.ByUser = append(report.ByUser, row.UsageAggregate)
		case "team":
			report.ByTeam = append(report.ByTeam, row.UsageAggregate)
		case "organization":
			report.ByOrganization = append(report.ByOrganization, row.UsageAggregate)
		}
	}
	if err := scanner.Err(); err != nil {
		return UsageReport{}, fmt.Errorf("decode usage report: %w", err)
	}
	return report, nil
}

func usageReportQuery(table string, scopeType string, filterModel, filterUpstreamModel, filterProvider, filterEndpoint, filterTag bool) string {
	where := "timestamp_unix >= {from:UInt64} AND timestamp_unix < {to:UInt64} AND phase IN ('commit','cancel')"
	columns := map[string]string{"key": "api_key_fingerprint", "user": "user_id", "team": "team_id", "organization": "organization_id"}
	if column := columns[scopeType]; column != "" {
		where += " AND " + column + " = {scope_id:String}"
	}
	if filterModel {
		where += " AND " + canonicalUsageModelExpression + " = {model:String}"
	}
	if filterUpstreamModel {
		where += " AND upstream_model = {upstream_model:String}"
	}
	if filterProvider {
		where += " AND " + canonicalUsageProviderExpression + " = {provider:String}"
	}
	if filterEndpoint {
		where += " AND provider_endpoint_name = {endpoint:String}"
	}
	if filterTag {
		where += " AND has(tags, {tag:String})"
	}
	// Qualify the source cost column because ClickHouse expands the `cost` result
	// alias inside later expressions after ARRAY JOIN and otherwise reports a
	// nested aggregate (sum(sum(cost))).
	metrics := "count() AS requests, countIf(status != 'ok') AS errors, sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens, sum(total_tokens) AS total_tokens, sum(usage.cost) AS cost, avg(latency_ms) AS avg_latency_ms, countIf(cache_status = 'hit') AS cache_hits, if(count() = 0, 0, sum(usage.cost) / count()) AS cost_per_request"
	return fmt.Sprintf(`
SELECT 'total' AS kind, '' AS date, '' AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY currency
UNION ALL SELECT 'day' AS kind, toString(toDate(parseDateTimeBestEffort(timestamp))) AS date, '' AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY date,currency
UNION ALL SELECT 'model' AS kind, '' AS date, %s AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
UNION ALL SELECT 'upstream_model' AS kind, '' AS date, if(upstream_model = '', 'Unassigned', upstream_model) AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
UNION ALL SELECT 'provider' AS kind, '' AS date, %s AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
UNION ALL SELECT 'endpoint' AS kind, '' AS date, if(provider_endpoint_name = '', 'Unassigned', provider_endpoint_name) AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
UNION ALL SELECT 'tag' AS kind, '' AS date, tag AS name, currency, %s FROM %s AS usage ARRAY JOIN if(empty(tags), ['Untagged'], tags) AS tag WHERE %s GROUP BY name,currency
UNION ALL SELECT 'key' AS kind, '' AS date, if(api_key_fingerprint = '', 'Unassigned', api_key_fingerprint) AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
UNION ALL SELECT 'user' AS kind, '' AS date, if(user_id = '', 'Unassigned', user_id) AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
UNION ALL SELECT 'team' AS kind, '' AS date, if(team_id = '', 'Unassigned', team_id) AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
UNION ALL SELECT 'organization' AS kind, '' AS date, if(organization_id = '', 'Unassigned', organization_id) AS name, currency, %s FROM %s AS usage WHERE %s GROUP BY name,currency
ORDER BY kind,date,cost DESC,total_tokens DESC
FORMAT JSONEachRow`, metrics, table, where, metrics, table, where, canonicalUsageModelExpression, metrics, table, where, metrics, table, where, canonicalUsageProviderExpression, metrics, table, where, metrics, table, where, metrics, table, where, metrics, table, where, metrics, table, where, metrics, table, where, metrics, table, where)
}
