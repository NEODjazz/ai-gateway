package gateway

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-gateway-gateway/internal/provider"
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

type UsageManagementClient interface {
	UsageReport(context.Context, ManagementAudit, int) (UsageReport, error)
}

type ScopedUsageManagementClient interface {
	ScopedUsageReport(context.Context, ManagementAudit, int, string, string) (UsageReport, error)
}

func (c *RemoteBudgetManagementClient) UsageReport(ctx context.Context, audit ManagementAudit, days int) (UsageReport, error) {
	var report UsageReport
	err := c.call(ctx, http.MethodGet, "/internal/v1/usage/report?days="+strconv.Itoa(days), audit, nil, &report)
	return report, err
}

func (c *RemoteBudgetManagementClient) ScopedUsageReport(ctx context.Context, audit ManagementAudit, days int, scopeType, scopeID string) (UsageReport, error) {
	query := url.Values{"days": {strconv.Itoa(days)}, "scope_type": {scopeType}, "scope_id": {scopeID}}
	var report UsageReport
	err := c.call(ctx, http.MethodGet, "/internal/v1/usage/report?"+query.Encode(), audit, nil, &report)
	return report, err
}

func (h Handler) WithUsageReporting(client UsageManagementClient) Handler {
	h.usage = client
	return h
}

func (h Handler) GetUsageReport(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	if h.usage == nil {
		writeError(w, http.StatusServiceUnavailable, "usage_unavailable", "usage reporting is not configured")
		return
	}
	days := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 90 {
			writeError(w, http.StatusBadRequest, "invalid_request", "days must be between 1 and 90")
			return
		}
		days = parsed
	}
	report, err := h.usage.UsageReport(r.Context(), managementAudit(req), days)
	if err != nil {
		writeManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.normalizeUsageProviders(r.Context(), report))
}

func (h Handler) GetCustomerUsageReport(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizeAdmin(w, r)
	if !ok {
		return
	}
	client, ok := h.usage.(ScopedUsageManagementClient)
	if !ok || client == nil {
		writeError(w, http.StatusServiceUnavailable, "usage_unavailable", "scoped usage reporting is not configured")
		return
	}
	scopeType, scopeID := strings.TrimSpace(r.PathValue("scope_type")), strings.TrimSpace(r.PathValue("scope_id"))
	if (scopeType != "key" && scopeType != "user" && scopeType != "team") || scopeID == "" || len(scopeID) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_request", "scope must be key, user, or team with a non-empty ID")
		return
	}
	days := 30
	if raw := strings.TrimSpace(r.URL.Query().Get("days")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 90 {
			writeError(w, http.StatusBadRequest, "invalid_request", "days must be between 1 and 90")
			return
		}
		days = parsed
	}
	report, err := client.ScopedUsageReport(r.Context(), managementAudit(req), days, scopeType, scopeID)
	if err != nil {
		writeManagementFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, h.normalizeUsageProviders(r.Context(), report))
}

func (h Handler) normalizeUsageProviders(ctx context.Context, report UsageReport) UsageReport {
	controller, ok := h.provider.(provider.DeploymentController)
	if !ok {
		return report
	}
	aliases := map[string]string{}
	for _, deployment := range controller.ListModelDeployments(ctx) {
		if deployment.ID != "" && deployment.ProviderID != "" {
			aliases[deployment.ID] = deployment.ProviderID
		}
	}
	merged := map[string]UsageAggregate{}
	for _, item := range report.ByProvider {
		if providerID := aliases[item.Name]; providerID != "" {
			item.Name = providerID
		}
		key := item.Name + "\x00" + item.Currency
		current := merged[key]
		latencyTotal := current.AvgLatencyMS*float64(current.Requests) + item.AvgLatencyMS*float64(item.Requests)
		current.Name, current.Currency = item.Name, item.Currency
		current.Requests += item.Requests
		current.Errors += item.Errors
		current.InputTokens += item.InputTokens
		current.OutputTokens += item.OutputTokens
		current.TotalTokens += item.TotalTokens
		current.Cost += item.Cost
		if current.Requests > 0 {
			current.AvgLatencyMS = latencyTotal / float64(current.Requests)
		}
		merged[key] = current
	}
	report.ByProvider = report.ByProvider[:0]
	for _, item := range merged {
		report.ByProvider = append(report.ByProvider, item)
	}
	sort.Slice(report.ByProvider, func(i, j int) bool {
		if report.ByProvider[i].Cost == report.ByProvider[j].Cost {
			return report.ByProvider[i].TotalTokens > report.ByProvider[j].TotalTokens
		}
		return report.ByProvider[i].Cost > report.ByProvider[j].Cost
	})
	return report
}
