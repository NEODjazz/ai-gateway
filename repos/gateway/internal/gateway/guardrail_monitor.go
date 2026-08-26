package gateway

import (
	"context"
	"errors"
	"net/http"
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
	Total       uint64 `json:"total"`
	Passed      uint64 `json:"passed"`
	Rejected    uint64 `json:"rejected"`
	Unavailable uint64 `json:"unavailable"`
}

type GuardrailMonitorSnapshot struct {
	Retention     int                         `json:"retention"`
	Summary       GuardrailSummary            `json:"summary"`
	ByModule      map[string]GuardrailSummary `json:"by_module"`
	Events        []GuardrailEvent            `json:"events"`
	ContentStored bool                        `json:"content_stored"`
}

type GuardrailMonitor struct {
	mu       sync.RWMutex
	capacity int
	events   []GuardrailEvent
	summary  GuardrailSummary
	byModule map[string]GuardrailSummary
}

func NewGuardrailMonitor(capacity int) *GuardrailMonitor {
	if capacity < 1 || capacity > 10000 {
		capacity = 200
	}
	return &GuardrailMonitor{capacity: capacity, byModule: map[string]GuardrailSummary{}}
}

func (m *GuardrailMonitor) Record(event GuardrailEvent) {
	if m == nil {
		return
	}
	event.RequestID = boundedMonitorValue(event.RequestID, 128)
	event.Policy = boundedMonitorValue(event.Policy, 128)
	event.Module = boundedMonitorValue(event.Module, 32)
	event.Source = boundedMonitorValue(event.Source, 32)
	if (event.Module != "dlp" && event.Module != "av") || (event.Outcome != "passed" && event.Outcome != "rejected" && event.Outcome != "unavailable") {
		return
	}
	if event.Source != "compliance" {
		event.Source = "inference"
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	m.mu.Lock()
	m.events = append(m.events, event)
	if len(m.events) > m.capacity {
		copy(m.events, m.events[len(m.events)-m.capacity:])
		m.events = m.events[:m.capacity]
	}
	incrementGuardrailSummary(&m.summary, event.Outcome)
	module := m.byModule[event.Module]
	incrementGuardrailSummary(&module, event.Outcome)
	m.byModule[event.Module] = module
	m.mu.Unlock()
}

func (m *GuardrailMonitor) Snapshot(limit int) GuardrailMonitorSnapshot {
	if limit < 1 || limit > m.capacity {
		limit = m.capacity
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	start := len(m.events) - limit
	if start < 0 {
		start = 0
	}
	events := append([]GuardrailEvent(nil), m.events[start:]...)
	for left, right := 0, len(events)-1; left < right; left, right = left+1, right-1 {
		events[left], events[right] = events[right], events[left]
	}
	byModule := make(map[string]GuardrailSummary, len(m.byModule))
	for name, summary := range m.byModule {
		byModule[name] = summary
	}
	return GuardrailMonitorSnapshot{Retention: m.capacity, Summary: m.summary, ByModule: byModule, Events: events, ContentStored: false}
}

func incrementGuardrailSummary(summary *GuardrailSummary, outcome string) {
	summary.Total++
	switch outcome {
	case "passed":
		summary.Passed++
	case "rejected":
		summary.Rejected++
	case "unavailable":
		summary.Unavailable++
	}
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
	m.monitor.Record(GuardrailEvent{RequestID: req.RequestID, Policy: req.Metadata["provider.guardrail.policy"], Module: m.Name(), Source: req.Metadata["guardrail.monitor.source"], Outcome: outcome, DurationMS: time.Since(started).Milliseconds()})
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
	writeJSON(w, http.StatusOK, h.guardrails.Snapshot(limit))
}
