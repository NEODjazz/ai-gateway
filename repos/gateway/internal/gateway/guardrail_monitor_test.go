package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
)

type monitoredGuardrailModule struct {
	name string
	err  error
}

type monitoredPostGuardrailModule struct{ monitoredGuardrailModule }

func (monitoredPostGuardrailModule) PostResponseEnabled() bool { return true }
func (m monitoredPostGuardrailModule) HandlePostResponse(context.Context, *modules.RequestContext) error {
	return m.err
}

type sharedGuardrailEventStore struct {
	mu       sync.Mutex
	events   []GuardrailEvent
	readErr  error
	writeErr error
}

func (s *sharedGuardrailEventStore) Append(_ context.Context, event GuardrailEvent, capacity int) error {
	if s.writeErr != nil {
		return s.writeErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append([]GuardrailEvent{event}, s.events...)
	if len(s.events) > capacity {
		s.events = s.events[:capacity]
	}
	return nil
}

func (s *sharedGuardrailEventStore) Recent(_ context.Context, limit int) ([]GuardrailEvent, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit > len(s.events) {
		limit = len(s.events)
	}
	return append([]GuardrailEvent(nil), s.events[:limit]...), nil
}

func (m monitoredGuardrailModule) Name() string   { return m.name }
func (m monitoredGuardrailModule) Required() bool { return true }
func (m monitoredGuardrailModule) Handle(context.Context, *modules.RequestContext) error {
	return m.err
}

func TestGuardrailMonitorRecordsOnlySafeMetadata(t *testing.T) {
	monitor := NewGuardrailMonitor(2)
	module := NewGuardrailMonitoringModule(monitoredGuardrailModule{name: "dlp", err: modules.ErrContentRejected}, monitor)
	req := &modules.RequestContext{
		RequestID: "request-1",
		Metadata: map[string]string{
			"provider.modules.dlp.enabled": "true",
			"provider.guardrail.policy":    "strict",
			"guardrail.monitor.source":     "compliance",
		},
	}
	if err := module.Handle(context.Background(), req); !errors.Is(err, modules.ErrContentRejected) {
		t.Fatalf("unexpected module error: %v", err)
	}
	snapshot := monitor.Snapshot(10)
	if snapshot.ContentStored || snapshot.Summary.Rejected != 1 || len(snapshot.Events) != 1 || snapshot.Events[0].Policy != "strict" || snapshot.Events[0].Source != "compliance" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	monitor.Record(GuardrailEvent{RequestID: "request-2", Module: "av", Outcome: "passed"})
	monitor.Record(GuardrailEvent{RequestID: "request-3", Module: "av", Outcome: "unavailable"})
	if events := monitor.Snapshot(10).Events; len(events) != 2 || events[0].RequestID != "request-3" {
		t.Fatalf("retention/newest order mismatch: %+v", events)
	}
}

func TestGuardrailMonitorSkipsDisabledModule(t *testing.T) {
	monitor := NewGuardrailMonitor(10)
	module := NewGuardrailMonitoringModule(monitoredGuardrailModule{name: "av"}, monitor)
	if err := module.Handle(context.Background(), &modules.RequestContext{Metadata: map[string]string{"provider.modules.av.enabled": "false"}}); err != nil {
		t.Fatal(err)
	}
	if monitor.Snapshot(10).Summary.Total != 0 {
		t.Fatal("disabled guardrail was recorded as a scan")
	}
}

func TestGuardrailMonitorRecordsProviderOutputPhase(t *testing.T) {
	monitor := NewGuardrailMonitor(10)
	module := NewGuardrailMonitoringModule(monitoredPostGuardrailModule{monitoredGuardrailModule{name: "dlp", err: modules.ErrContentRejected}}, monitor)
	post, ok := module.(modules.PostResponseModule)
	if !ok || !post.PostResponseEnabled() {
		t.Fatal("monitor wrapper did not preserve post-response module")
	}
	req := &modules.RequestContext{RequestID: "request-output", Metadata: map[string]string{"provider.modules.dlp.output_enabled": "true", "provider.guardrail.policy": "strict"}}
	if err := post.HandlePostResponse(context.Background(), req); !errors.Is(err, modules.ErrContentRejected) {
		t.Fatalf("unexpected output guardrail error: %v", err)
	}
	snapshot := monitor.Snapshot(10)
	if snapshot.Summary.Rejected != 1 || len(snapshot.Events) != 1 || snapshot.Events[0].Source != "inference_output" {
		t.Fatalf("unexpected output monitor snapshot: %+v", snapshot)
	}
}

func TestGuardrailMonitorAdminAPIExcludesContent(t *testing.T) {
	monitor := NewGuardrailMonitor(10)
	monitor.Record(GuardrailEvent{RequestID: "safe-request", Policy: "strict", Module: "dlp", Outcome: "passed"})
	handler := NewHandler(modulesPipeline("admin"), nil).WithGuardrailMonitor(monitor)
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/v1/guardrails/monitor?limit=5", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"content_stored":false`) || !strings.Contains(body, `"request_id":"safe-request"`) || strings.Contains(body, "prompt") || strings.Contains(body, "response_body") {
		t.Fatalf("unsafe monitoring response: status=%d body=%s", response.Code, body)
	}
}

func TestGuardrailMonitorConcurrentRecording(t *testing.T) {
	monitor := NewGuardrailMonitor(50)
	var group sync.WaitGroup
	for index := 0; index < 100; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			monitor.Record(GuardrailEvent{Module: "dlp", Outcome: "passed"})
		}()
	}
	group.Wait()
	snapshot := monitor.Snapshot(50)
	if snapshot.Summary.Total != 100 || len(snapshot.Events) != 50 {
		t.Fatalf("unexpected concurrent snapshot: %+v", snapshot)
	}
}

func TestGuardrailMonitorBuildsFilteredSharedReport(t *testing.T) {
	store := &sharedGuardrailEventStore{}
	first := NewGuardrailMonitorWithStore(10, store)
	second := NewGuardrailMonitorWithStore(10, store)
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	second.now = func() time.Time { return now }
	first.Record(GuardrailEvent{OccurredAt: now.Add(-2 * time.Hour), RequestID: "old", Policy: "baseline", Module: "dlp", Source: "inference", Outcome: "passed", DurationMS: 10})
	first.Record(GuardrailEvent{OccurredAt: now.Add(-10 * time.Minute), RequestID: "blocked", Policy: "strict", Module: "av", Source: "inference", Outcome: "rejected", DurationMS: 20})
	first.Record(GuardrailEvent{OccurredAt: now.Add(-5 * time.Minute), RequestID: "unavailable", Policy: "strict", Module: "dlp", Source: "compliance", Outcome: "unavailable", DurationMS: 40})

	report := second.Report(context.Background(), GuardrailMonitorFilter{Window: "15m", Policy: "strict"}, 10)
	if report.Scope != "shared_redis" || !report.StoreAvailable || report.RetainedEvents != 3 || report.FilteredSummary.Total != 2 || report.FilteredSummary.Rejected != 1 || report.FilteredSummary.Unavailable != 1 || report.FilteredSummary.AverageDurationMS != 30 {
		t.Fatalf("unexpected shared report: %+v", report)
	}
	if report.Summary.Total != 0 {
		t.Fatalf("second-replica process summary was polluted: %+v", report.Summary)
	}
	if len(report.Events) != 2 || report.Events[0].RequestID != "unavailable" || report.ByPolicy["strict"].Total != 2 || len(report.Timeline) != 2 {
		t.Fatalf("unexpected filtered rows: %+v", report)
	}

	dlpOnly := second.Report(context.Background(), GuardrailMonitorFilter{Window: "24h", Module: "dlp", Source: "compliance"}, 10)
	if dlpOnly.FilteredSummary.Total != 1 || len(dlpOnly.Events) != 1 || dlpOnly.Events[0].RequestID != "unavailable" {
		t.Fatalf("module/source filter mismatch: %+v", dlpOnly)
	}
}

func TestGuardrailMonitorFallsBackToCurrentReplicaWhenSharedStoreFails(t *testing.T) {
	store := &sharedGuardrailEventStore{readErr: errors.New("redis unavailable"), writeErr: errors.New("redis unavailable")}
	monitor := NewGuardrailMonitorWithStore(10, store)
	monitor.Record(GuardrailEvent{RequestID: "local", Module: "dlp", Outcome: "passed"})
	report := monitor.Report(context.Background(), GuardrailMonitorFilter{Window: "retained"}, 10)
	if report.Scope != "current_replica" || report.StoreAvailable || report.StoreErrors != 2 || report.FilteredSummary.Total != 1 || len(report.Events) != 1 {
		t.Fatalf("unexpected local fallback: %+v", report)
	}
}

func TestGuardrailMonitorAdminAPIValidatesFilters(t *testing.T) {
	monitor := NewGuardrailMonitor(10)
	handler := Routes(NewHandler(modulesPipeline("admin"), nil).WithGuardrailMonitor(monitor))
	for _, path := range []string{
		"/admin/v1/guardrails/monitor?window=30d",
		"/admin/v1/guardrails/monitor?module=unknown",
		"/admin/v1/guardrails/monitor?outcome=blocked",
		"/admin/v1/guardrails/monitor?source=browser",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid filter accepted: path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}
