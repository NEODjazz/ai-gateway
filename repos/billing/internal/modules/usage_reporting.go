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
	"strings"
	"time"
)

type UsageAggregate struct {
	Date         string  `json:"date,omitempty"`
	Name         string  `json:"name,omitempty"`
	Currency     string  `json:"currency"`
	Requests     uint64  `json:"requests"`
	Errors       uint64  `json:"errors"`
	InputTokens  uint64  `json:"input_tokens"`
	OutputTokens uint64  `json:"output_tokens"`
	TotalTokens  uint64  `json:"total_tokens"`
	Cost         float64 `json:"cost"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

type UsageReport struct {
	Days       int              `json:"days"`
	From       time.Time        `json:"from"`
	To         time.Time        `json:"to"`
	Totals     []UsageAggregate `json:"totals"`
	Daily      []UsageAggregate `json:"daily"`
	ByModel    []UsageAggregate `json:"by_model"`
	ByProvider []UsageAggregate `json:"by_provider"`
}

type UsageReporter interface {
	Report(context.Context, int) (UsageReport, error)
}

type UsageScope struct {
	Type string
	ID   string
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
	return r.report(ctx, days, UsageScope{})
}

func (r *ClickHouseUsageReporter) ReportScoped(ctx context.Context, days int, scope UsageScope) (UsageReport, error) {
	if (scope.Type != "key" && scope.Type != "user" && scope.Type != "team") || strings.TrimSpace(scope.ID) == "" || len(scope.ID) > 256 {
		return UsageReport{}, errors.New("invalid usage scope")
	}
	return r.report(ctx, days, scope)
}

func (r *ClickHouseUsageReporter) report(ctx context.Context, days int, scope UsageScope) (UsageReport, error) {
	if r == nil || r.client == nil || days < 1 || days > 90 {
		return UsageReport{}, errors.New("usage report days must be between 1 and 90")
	}
	to := r.now().UTC()
	report := UsageReport{Days: days, From: to.AddDate(0, 0, -days), To: to, Totals: []UsageAggregate{}, Daily: []UsageAggregate{}, ByModel: []UsageAggregate{}, ByProvider: []UsageAggregate{}}
	query := usageReportQuery(r.table, days, scope.Type)
	parameters := url.Values{"output_format_json_quote_64bit_integers": {"0"}, "query": {query}}
	if scope.Type != "" {
		parameters.Set("param_scope_id", scope.ID)
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
		case "provider":
			report.ByProvider = append(report.ByProvider, row.UsageAggregate)
		}
	}
	if err := scanner.Err(); err != nil {
		return UsageReport{}, fmt.Errorf("decode usage report: %w", err)
	}
	return report, nil
}

func usageReportQuery(table string, days int, scopeType string) string {
	where := fmt.Sprintf("timestamp_unix >= toUnixTimestamp(now() - INTERVAL %d DAY) AND phase IN ('commit','cancel')", days)
	columns := map[string]string{"key": "credential_id", "user": "user_id", "team": "team_id"}
	if column := columns[scopeType]; column != "" {
		where += " AND " + column + " = {scope_id:String}"
	}
	metrics := "count() AS requests, countIf(status != 'ok') AS errors, sum(input_tokens) AS input_tokens, sum(output_tokens) AS output_tokens, sum(total_tokens) AS total_tokens, sum(cost) AS cost, avg(latency_ms) AS avg_latency_ms"
	return fmt.Sprintf(`
SELECT 'total' AS kind, '' AS date, '' AS name, currency, %s FROM %s WHERE %s GROUP BY currency
UNION ALL SELECT 'day' AS kind, toString(toDate(parseDateTimeBestEffort(timestamp))) AS date, '' AS name, currency, %s FROM %s WHERE %s GROUP BY date,currency
UNION ALL SELECT 'model' AS kind, '' AS date, model AS name, currency, %s FROM %s WHERE %s GROUP BY name,currency
UNION ALL SELECT 'provider' AS kind, '' AS date, if(provider_endpoint_name != '',provider_endpoint_name,provider) AS name, currency, %s FROM %s WHERE %s GROUP BY name,currency
ORDER BY kind,date,cost DESC,total_tokens DESC
FORMAT JSONEachRow`, metrics, table, where, metrics, table, where, metrics, table, where, metrics, table, where)
}
