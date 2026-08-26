package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-gateway-gateway/internal/modules"
)

type monitoredGuardrailModule struct {
	name string
	err  error
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
