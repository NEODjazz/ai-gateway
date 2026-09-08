package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type UsageEventWriter interface {
	WriteUsageEvent(ctx context.Context, event BillingEvent) error
}

type PolicyChecker interface {
	Apply(ctx context.Context, event *BillingEvent) error
	Ready(ctx context.Context) error
	Close()
}

type NoopUsageEventWriter struct{}

func (NoopUsageEventWriter) WriteUsageEvent(context.Context, BillingEvent) error {
	return nil
}

type NoopPolicyChecker struct{}

func (NoopPolicyChecker) Apply(context.Context, *BillingEvent) error {
	return nil
}
func (NoopPolicyChecker) Ready(context.Context) error { return nil }
func (NoopPolicyChecker) Close()                      {}

type NotConfiguredPolicyChecker struct {
	settings Settings
}

func (c NotConfiguredPolicyChecker) Apply(context.Context, *BillingEvent) error {
	var enabled []string
	if c.settings.TariffsEnabled {
		enabled = append(enabled, "tariffs")
	}
	if c.settings.LimitsEnabled {
		enabled = append(enabled, "limits")
	}
	if c.settings.QuotasEnabled {
		enabled = append(enabled, "quotas")
	}
	if c.settings.FinancialTransactionsEnabled {
		enabled = append(enabled, "financial_transactions")
	}
	if len(enabled) == 0 {
		return nil
	}
	if c.settings.PostgresDSN == "" {
		return fmt.Errorf("postgres policy store is required when %s are enabled", strings.Join(enabled, ","))
	}
	return errors.New("postgres policy store is not implemented yet")
}
func (c NotConfiguredPolicyChecker) Ready(ctx context.Context) error {
	return c.Apply(ctx, &BillingEvent{})
}
func (NotConfiguredPolicyChecker) Close() {}

type ClickHouseUsageEventWriter struct {
	endpoint string
	username string
	password string
	client   *http.Client
}

func NewUsageEventWriter(settings Settings) UsageEventWriter {
	if !settings.UsageEventsEnabled {
		return NoopUsageEventWriter{}
	}
	return NewAsyncUsageOutbox(NewClickHouseUsageEventWriter(settings), 1024, 3)
}

func NewPolicyChecker(settings Settings) PolicyChecker {
	if !settings.TariffsEnabled && !settings.LimitsEnabled && !settings.QuotasEnabled && !settings.FinancialTransactionsEnabled {
		return NoopPolicyChecker{}
	}
	if !settings.TariffsEnabled && !settings.FinancialTransactionsEnabled && (settings.LimitsEnabled || settings.QuotasEnabled) {
		return NewPostgresBudgetPolicyChecker(settings.PostgresDSN, settings.ReservationTTL)
	}
	return NotConfiguredPolicyChecker{settings: settings}
}

func NewClickHouseUsageEventWriter(settings Settings) ClickHouseUsageEventWriter {
	query := fmt.Sprintf("INSERT INTO %s.%s FORMAT JSONEachRow", settings.ClickHouseDatabase, settings.ClickHouseUsageEventsTable)
	baseURL := strings.TrimRight(settings.ClickHouseURL, "/")
	return ClickHouseUsageEventWriter{
		endpoint: baseURL + "/?query=" + url.QueryEscape(query),
		username: settings.ClickHouseUsername,
		password: settings.ClickHousePassword,
		client: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

func (w ClickHouseUsageEventWriter) WriteUsageEvent(ctx context.Context, event BillingEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	body = append(body, '\n')

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.username != "" || w.password != "" {
		req.SetBasicAuth(w.username, w.password)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("clickhouse returned %s", resp.Status)
	}
	return nil
}
