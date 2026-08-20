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
	"sync"
	"time"
)

type UsageEventWriter interface {
	WriteUsageEvent(ctx context.Context, event BillingEvent) error
}

type PolicyChecker interface {
	Check(ctx context.Context, event BillingEvent) error
}

type NoopUsageEventWriter struct{}

func (NoopUsageEventWriter) WriteUsageEvent(context.Context, BillingEvent) error {
	return nil
}

type NoopPolicyChecker struct{}

func (NoopPolicyChecker) Check(context.Context, BillingEvent) error {
	return nil
}

type LifecycleStore struct {
	mu   sync.Mutex
	seen map[string]struct{}
}

func NewLifecycleStore() *LifecycleStore {
	return &LifecycleStore{seen: map[string]struct{}{}}
}

func (s *LifecycleStore) Begin(key string) bool {
	if s == nil || key == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.seen[key]; exists {
		return false
	}
	s.seen[key] = struct{}{}
	return true
}

func (s *LifecycleStore) Release(key string) {
	if s == nil || key == "" {
		return
	}
	s.mu.Lock()
	delete(s.seen, key)
	s.mu.Unlock()
}

type AsyncUsageOutbox struct {
	writer  UsageEventWriter
	queue   chan BillingEvent
	retries int
}

func NewAsyncUsageOutbox(writer UsageEventWriter, capacity int, retries int) *AsyncUsageOutbox {
	if capacity <= 0 {
		capacity = 1024
	}
	outbox := &AsyncUsageOutbox{writer: writer, queue: make(chan BillingEvent, capacity), retries: retries}
	go outbox.run()
	return outbox
}

func (o *AsyncUsageOutbox) WriteUsageEvent(_ context.Context, event BillingEvent) error {
	select {
	case o.queue <- event:
		return nil
	default:
		return errors.New("usage outbox is full")
	}
}

func (o *AsyncUsageOutbox) run() {
	for event := range o.queue {
		for attempt := 0; attempt <= o.retries; attempt++ {
			if err := o.writer.WriteUsageEvent(context.Background(), event); err == nil {
				break
			}
			if attempt < o.retries {
				time.Sleep(time.Duration(attempt+1) * 50 * time.Millisecond)
			}
		}
	}
}

type NotConfiguredPolicyChecker struct {
	settings Settings
}

func (c NotConfiguredPolicyChecker) Check(context.Context, BillingEvent) error {
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
