package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

type complianceModule struct {
	name   string
	reject bool
	seen   string
}

func (m *complianceModule) Name() string   { return m.name }
func (m *complianceModule) Required() bool { return true }
func (m *complianceModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.seen = req.Request.Messages[0].Content.(string)
	if m.reject {
		return modules.ErrContentRejected
	}
	return nil
}

func TestGuardrailPolicyAndCompliancePlayground(t *testing.T) {
	runtime := provider.New(provider.Config{})
	dlp := &complianceModule{name: "dlp"}
	av := &complianceModule{name: "av", reject: true}
	monitor := NewGuardrailMonitor(10)
	handler := NewHandler(modulesPipeline("admin"), runtime).WithComplianceModules(NewGuardrailMonitoringModule(dlp, monitor), NewGuardrailMonitoringModule(av, monitor)).WithGuardrailMonitor(monitor)
	put := httptest.NewRecorder()
	Routes(handler).ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/admin/v1/guardrail-policies/strict", strings.NewReader(`{"description":"test","dlp":true,"av":true,"enabled":true}`)))
	if put.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
	check := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/compliance/check", strings.NewReader(`{"policy":"strict","text":"sensitive fixture"}`))
	request.Header.Set("X-Request-ID", "compliance-1")
	Routes(handler).ServeHTTP(check, request)
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), `"allowed":false`) || !strings.Contains(check.Body.String(), `"content_stored":false`) || strings.Contains(check.Body.String(), "sensitive fixture") || dlp.seen != "sensitive fixture" || av.seen != "sensitive fixture" {
		t.Fatalf("unsafe compliance result: status=%d body=%s dlp=%q av=%q", check.Code, check.Body.String(), dlp.seen, av.seen)
	}
	snapshot := monitor.Snapshot(10)
	if snapshot.Summary.Total != 2 || snapshot.Summary.Rejected != 1 || snapshot.Events[0].Source != "compliance" || snapshot.Events[0].Policy != "strict" {
		t.Fatalf("compliance outcomes were not monitored safely: %+v", snapshot)
	}
}

func TestComplianceUnavailableFailsClosed(t *testing.T) {
	runtime := provider.New(provider.Config{})
	_, _ = runtime.(provider.GuardrailController).UpdateGuardrailPolicy("strict", provider.GuardrailPolicy{DLP: true, Enabled: true})
	broken := &complianceErrorModule{}
	handler := NewHandler(modulesPipeline("admin"), runtime).WithComplianceModules(broken, &complianceModule{name: "av"})
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/compliance/check", strings.NewReader(`{"policy":"strict","text":"fixture"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type complianceErrorModule struct{}

func (*complianceErrorModule) Name() string   { return "dlp" }
func (*complianceErrorModule) Required() bool { return true }
func (*complianceErrorModule) Handle(context.Context, *modules.RequestContext) error {
	return errors.New("scanner unavailable")
}
