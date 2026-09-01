package gateway

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type GuardrailEvent struct {
	OccurredAt time.Time `json:"occurred_at"`
	RequestID  string    `json:"request_id,omitempty"`
	Policy     string    `json:"policy,omitempty"`
	Module     string    `json:"module"`
	Source     string    `json:"source"`
	Outcome    string    `json:"outcome"`
	DurationMS int64     `json:"duration_ms"`
}

type GuardrailSummary struct {
	Total             uint64  `json:"total"`
	Passed            uint64  `json:"passed"`
	Rejected          uint64  `json:"rejected"`
	Unavailable       uint64  `json:"unavailable"`
	DurationMS        uint64  `json:"duration_ms"`
	AverageDurationMS float64 `json:"average_duration_ms"`
}

type GuardrailMonitorFilter struct {
	Window  string `json:"window"`
	Module  string `json:"module,omitempty"`
	Policy  string `json:"policy,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	Source  string `json:"source,omitempty"`
}

type GuardrailTimelineBucket struct {
	StartedAt time.Time        `json:"started_at"`
	Summary   GuardrailSummary `json:"summary"`
}

type GuardrailMonitorSnapshot struct {
	Retention        int                         `json:"retention"`
	RetainedEvents   int                         `json:"retained_events"`
	RetentionFull    bool                        `json:"retention_full"`
	Scope            string                      `json:"scope"`
	StoreAvailable   bool                        `json:"store_available"`
	StoreErrors      uint64                      `json:"store_errors"`
	StartedAt        time.Time                   `json:"started_at"`
	OldestRetainedAt *time.Time                  `json:"oldest_retained_at,omitempty"`
	Filters          GuardrailMonitorFilter      `json:"filters"`
	Summary          GuardrailSummary            `json:"summary"`
	ByModule         map[string]GuardrailSummary `json:"by_module"`
	FilteredSummary  GuardrailSummary            `json:"filtered_summary"`
	FilteredByModule map[string]GuardrailSummary `json:"filtered_by_module"`
	ByPolicy         map[string]GuardrailSummary `json:"by_policy"`
	Timeline         []GuardrailTimelineBucket   `json:"timeline"`
	Events           []GuardrailEvent            `json:"events"`
	ContentStored    bool                        `json:"content_stored"`
}

type GuardrailMonitor struct {
	mu       sync.RWMutex
	capacity int
	events   []GuardrailEvent
	summary  GuardrailSummary
	byModule map[string]GuardrailSummary
	started  time.Time
	store    GuardrailEventStore
	storeErr uint64
	now      func() time.Time
}

func NewGuardrailMonitor(capacity int) *GuardrailMonitor {
	return NewGuardrailMonitorWithStore(capacity, nil)
}

func NewGuardrailMonitorWithStore(capacity int, store GuardrailEventStore) *GuardrailMonitor {
	if capacity < 1 || capacity > 10000 {
		capacity = 200
	}
	now := time.Now().UTC()
	return &GuardrailMonitor{capacity: capacity, byModule: map[string]GuardrailSummary{}, started: now, store: store, now: func() time.Time { return time.Now().UTC() }}
}

func (m *GuardrailMonitor) Record(event GuardrailEvent) {
	m.RecordContext(context.Background(), event)
}

func (m *GuardrailMonitor) RecordContext(ctx context.Context, event GuardrailEvent) {
	if m == nil {
		return
	}
	normalized, ok := normalizeGuardrailEvent(event)
	if !ok {
		return
	}
	event = normalized
	m.mu.Lock()
	m.events = append(m.events, event)
	if len(m.events) > m.capacity {
		copy(m.events, m.events[len(m.events)-m.capacity:])
		m.events = m.events[:m.capacity]
	}
	incrementGuardrailSummary(&m.summary, event.Outcome, event.DurationMS)
	module := m.byModule[event.Module]
	incrementGuardrailSummary(&module, event.Outcome, event.DurationMS)
	m.byModule[event.Module] = module
	m.mu.Unlock()
	if m.store != nil {
		storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 100*time.Millisecond)
		err := m.store.Append(storeCtx, event, m.capacity)
		cancel()
		if err != nil {
			m.mu.Lock()
			m.storeErr++
			m.mu.Unlock()
		}
	}
}

func (m *GuardrailMonitor) Snapshot(limit int) GuardrailMonitorSnapshot {
	return m.Report(context.Background(), GuardrailMonitorFilter{Window: "retained"}, limit)
}

func (m *GuardrailMonitor) Report(ctx context.Context, filter GuardrailMonitorFilter, limit int) GuardrailMonitorSnapshot {
	if limit < 1 || limit > m.capacity {
		limit = m.capacity
	}
	m.mu.RLock()
	events := append([]GuardrailEvent(nil), m.events...)
	processSummary := m.summary
	byModule := make(map[string]GuardrailSummary, len(m.byModule))
	for name, summary := range m.byModule {
		byModule[name] = summary
	}
	started, storeErrors := m.started, m.storeErr
	m.mu.RUnlock()
	reverseGuardrailEvents(events)
	scope, storeAvailable := "current_replica", false
	if m.store != nil {
		storeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		shared, err := m.store.Recent(storeCtx, m.capacity)
		cancel()
		if err == nil {
			events, scope, storeAvailable = shared, "shared_redis", true
		} else {
			m.mu.Lock()
			m.storeErr++
			storeErrors = m.storeErr
			m.mu.Unlock()
		}
	}
	filter.Window = strings.TrimSpace(filter.Window)
	if filter.Window == "" {
		filter.Window = "retained"
	}
	now := m.now()
	filtered := make([]GuardrailEvent, 0, len(events))
	for _, event := range events {
		if guardrailEventMatches(event, filter, now) {
			filtered = append(filtered, event)
		}
	}
	filteredSummary, filteredByModule, byPolicy := summarizeGuardrailEvents(filtered)
	timeline := guardrailTimeline(filtered, filter.Window)
	visible := filtered
	if len(visible) > limit {
		visible = visible[:limit]
	}
	var oldest *time.Time
	if len(events) != 0 {
		value := events[len(events)-1].OccurredAt
		oldest = &value
	}
	return GuardrailMonitorSnapshot{
		Retention: m.capacity, RetainedEvents: len(events), RetentionFull: len(events) >= m.capacity,
		Scope: scope, StoreAvailable: storeAvailable, StoreErrors: storeErrors, StartedAt: started, OldestRetainedAt: oldest,
		Filters: filter, Summary: processSummary, ByModule: byModule, FilteredSummary: filteredSummary,
		FilteredByModule: filteredByModule, ByPolicy: byPolicy, Timeline: timeline, Events: visible, ContentStored: false,
	}
}

func incrementGuardrailSummary(summary *GuardrailSummary, outcome string, durationMS int64) {
	summary.Total++
	switch outcome {
	case "passed":
		summary.Passed++
	case "rejected":
		summary.Rejected++
	case "unavailable":
		summary.Unavailable++
	}
	if durationMS > 0 {
		summary.DurationMS += uint64(durationMS)
	}
	summary.AverageDurationMS = float64(summary.DurationMS) / float64(summary.Total)
}

func normalizeGuardrailEvent(event GuardrailEvent) (GuardrailEvent, bool) {
	event.RequestID = boundedMonitorValue(event.RequestID, 128)
	event.Policy = boundedMonitorValue(event.Policy, 128)
	event.Module = boundedMonitorValue(event.Module, 32)
	event.Source = boundedMonitorValue(event.Source, 32)
	if (event.Module != "dlp" && event.Module != "av") || (event.Outcome != "passed" && event.Outcome != "rejected" && event.Outcome != "unavailable") {
		return GuardrailEvent{}, false
	}
	if event.Source != "compliance" {
		event.Source = "inference"
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	} else {
		event.OccurredAt = event.OccurredAt.UTC()
	}
	if event.DurationMS < 0 {
		event.DurationMS = 0
	} else if event.DurationMS > int64(time.Hour/time.Millisecond) {
		event.DurationMS = int64(time.Hour / time.Millisecond)
	}
	return event, true
}

func reverseGuardrailEvents(events []GuardrailEvent) {
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
}

func guardrailEventMatches(event GuardrailEvent, filter GuardrailMonitorFilter, now time.Time) bool {
	window := map[string]time.Duration{"15m": 15 * time.Minute, "1h": time.Hour, "24h": 24 * time.Hour}[filter.Window]
	if window > 0 && event.OccurredAt.Before(now.Add(-window)) {
		return false
	}
	return (filter.Module == "" || event.Module == filter.Module) &&
		(filter.Policy == "" || event.Policy == filter.Policy) &&
		(filter.Outcome == "" || event.Outcome == filter.Outcome) &&
		(filter.Source == "" || event.Source == filter.Source)
}

func summarizeGuardrailEvents(events []GuardrailEvent) (GuardrailSummary, map[string]GuardrailSummary, map[string]GuardrailSummary) {
	byModule := map[string]GuardrailSummary{}
	byPolicy := map[string]GuardrailSummary{}
	var total GuardrailSummary
	for _, event := range events {
		incrementGuardrailSummary(&total, event.Outcome, event.DurationMS)
		module := byModule[event.Module]
		incrementGuardrailSummary(&module, event.Outcome, event.DurationMS)
		byModule[event.Module] = module
		policy := byPolicy[event.Policy]
		incrementGuardrailSummary(&policy, event.Outcome, event.DurationMS)
		byPolicy[event.Policy] = policy
	}
	return total, byModule, byPolicy
}

func guardrailTimeline(events []GuardrailEvent, window string) []GuardrailTimelineBucket {
	width := map[string]time.Duration{"15m": time.Minute, "1h": 5 * time.Minute, "24h": time.Hour}[window]
	if width == 0 {
		width = time.Hour
		if len(events) > 1 && events[0].OccurredAt.Sub(events[len(events)-1].OccurredAt) > 24*time.Hour {
			width = 24 * time.Hour
		}
	}
	buckets := map[time.Time]GuardrailSummary{}
	for _, event := range events {
		started := event.OccurredAt.Truncate(width)
		summary := buckets[started]
		incrementGuardrailSummary(&summary, event.Outcome, event.DurationMS)
		buckets[started] = summary
	}
	starts := make([]time.Time, 0, len(buckets))
	for started := range buckets {
		starts = append(starts, started)
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	result := make([]GuardrailTimelineBucket, 0, len(starts))
	for _, started := range starts {
		result = append(result, GuardrailTimelineBucket{StartedAt: started, Summary: buckets[started]})
	}
	return result
}

func boundedMonitorValue(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

type guardrailMonitoringModule struct {
	inner   modules.Module
	monitor *GuardrailMonitor
}

func NewGuardrailMonitoringModule(inner modules.Module, monitor *GuardrailMonitor) modules.Module {
	return guardrailMonitoringModule{inner: inner, monitor: monitor}
}

func (m guardrailMonitoringModule) Name() string   { return m.inner.Name() }
func (m guardrailMonitoringModule) Required() bool { return m.inner.Required() }
func (m guardrailMonitoringModule) Handle(ctx context.Context, req *modules.RequestContext) error {
	if !guardrailModuleEnabled(req.Metadata, m.Name()) {
		return m.inner.Handle(ctx, req)
	}
	started := time.Now()
	err := m.inner.Handle(ctx, req)
	outcome := "passed"
	if errors.Is(err, modules.ErrContentRejected) {
		outcome = "rejected"
	} else if err != nil {
		outcome = "unavailable"
	}
	m.monitor.RecordContext(ctx, GuardrailEvent{RequestID: req.RequestID, Policy: req.Metadata["provider.guardrail.policy"], Module: m.Name(), Source: req.Metadata["guardrail.monitor.source"], Outcome: outcome, DurationMS: time.Since(started).Milliseconds()})
	return err
}

func guardrailModuleEnabled(metadata map[string]string, module string) bool {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(metadata["provider.modules."+module+".enabled"]))
	return enabled
}

func (h Handler) WithGuardrailMonitor(monitor *GuardrailMonitor) Handler {
	h.guardrails = monitor
	return h
}

func (h Handler) GetGuardrailMonitor(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorizeAdmin(w, r); !ok {
		return
	}
	if h.guardrails == nil {
		writeError(w, http.StatusServiceUnavailable, "monitoring_unavailable", "guardrail monitoring is unavailable")
		return
	}
	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 1000 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 1000")
			return
		}
		limit = parsed
	}
	filter := GuardrailMonitorFilter{
		Window:  strings.TrimSpace(r.URL.Query().Get("window")),
		Module:  strings.TrimSpace(r.URL.Query().Get("module")),
		Policy:  strings.TrimSpace(r.URL.Query().Get("policy")),
		Outcome: strings.TrimSpace(r.URL.Query().Get("outcome")),
		Source:  strings.TrimSpace(r.URL.Query().Get("source")),
	}
	if filter.Window == "" {
		filter.Window = "retained"
	}
	if !allowedValue(filter.Window, "retained", "15m", "1h", "24h") || !allowedValue(filter.Module, "", "dlp", "av") || len(filter.Policy) > 128 || !allowedValue(filter.Outcome, "", "passed", "rejected", "unavailable") || !allowedValue(filter.Source, "", "inference", "compliance") {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid guardrail monitor filter")
		return
	}
	writeJSON(w, http.StatusOK, h.guardrails.Report(r.Context(), filter, limit))
}
