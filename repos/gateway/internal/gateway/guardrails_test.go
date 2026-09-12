package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

type complianceModule struct {
	name   string
	reject bool
	seen   string
}

type guardrailRateStore struct {
	allowed bool
	tokens  int
	key     string
}

func (s *guardrailRateStore) Allow(_ context.Context, key string, _ RateLimit, tokens int) (bool, time.Duration, error) {
	s.key, s.tokens = key, tokens
	return s.allowed, time.Minute, nil
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
	handler := NewHandler(modulesPipeline("admin"), runtime).WithComplianceModules(NewGuardrailMonitoringModule(dlp, monitor), NewGuardrailMonitoringModule(av, monitor)).WithAnonymizerModule(modules.NewAnonymizerModule(true, modules.RuleEmail)).WithGuardrailMonitor(monitor)
	put := httptest.NewRecorder()
	Routes(handler).ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/admin/v1/guardrail-policies/strict", strings.NewReader(`{"description":"test","dlp":true,"output_dlp":true,"av":true,"anonymization":"custom","anonymization_rules":["email"],"enabled":true}`)))
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), `"output_dlp":true`) {
		t.Fatalf("put status=%d body=%s", put.Code, put.Body.String())
	}
	rules := httptest.NewRecorder()
	Routes(handler).ServeHTTP(rules, httptest.NewRequest(http.MethodGet, "/admin/v1/anonymizer/rules", nil))
	if rules.Code != http.StatusOK || rules.Body.String() != "{\"data\":[\"email\"]}\n" {
		t.Fatalf("rules status=%d body=%s", rules.Code, rules.Body.String())
	}
	check := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/compliance/check", strings.NewReader(`{"policy":"strict","text":"user@example.com"}`))
	request.Header.Set("X-Request-ID", "compliance-1")
	Routes(handler).ServeHTTP(check, request)
	if check.Code != http.StatusOK || !strings.Contains(check.Body.String(), `"allowed":false`) || !strings.Contains(check.Body.String(), `"content_stored":false`) || !strings.Contains(check.Body.String(), `"anonymized_text":"{{EMAIL_1}}"`) || !strings.Contains(check.Body.String(), `"replacements":1`) || strings.Contains(check.Body.String(), "user@example.com") || dlp.seen != "user@example.com" || av.seen != "user@example.com" {
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
	handler := NewHandler(modulesPipeline("admin"), runtime).WithComplianceModules(broken, &complianceModule{name: "av"}).WithAnonymizerModule(modules.NewAnonymizerModule(true, "all"))
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/admin/v1/compliance/check", strings.NewReader(`{"policy":"strict","text":"fixture"}`)))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestApplyGuardrailUsesAttachedPolicyRateLimitMonitorAndAudit(t *testing.T) {
	runtime := provider.New(provider.Config{})
	_, _ = runtime.(provider.GuardrailController).UpdateGuardrailPolicy("strict", provider.GuardrailPolicy{DLP: true, AV: true, Enabled: true})
	access := NewAccessRegistry()
	if _, err := access.PutPolicyAttachment("key-policy", PolicyAttachment{PolicyName: "strict", Scope: "specific", Keys: []string{"admin-credential"}, Models: []string{"safe-model"}}); err != nil {
		t.Fatal(err)
	}
	dlp := &complianceModule{name: "dlp"}
	av := &complianceModule{name: "av", reject: true}
	monitor := NewGuardrailMonitor(10)
	audit := &recordingAuditClient{}
	rates := &guardrailRateStore{allowed: true}
	handler := NewHandlerWithRateLimitStore(modulesPipeline("inference"), runtime, rates).
		WithComplianceModules(NewGuardrailMonitoringModule(dlp, monitor), NewGuardrailMonitoringModule(av, monitor)).
		WithGuardrailMonitor(monitor).WithAccessRegistry(access).WithAudit(audit)

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/guardrails/apply_guardrail", strings.NewReader(`{"guardrail_name":"strict","model":"safe-model","text":"sensitive fixture"}`))
	request.Header.Set("Authorization", "Bearer key")
	Routes(handler).ServeHTTP(response, request)

	body := response.Body.String()
	if response.Code != http.StatusOK || !strings.Contains(body, `"execution_id":"`) || strings.Contains(body, `"request_id":`) || !strings.Contains(body, `"allowed":false`) || !strings.Contains(body, `"av":"rejected"`) || !strings.Contains(body, `"content_stored":false`) || strings.Contains(body, "sensitive fixture") {
		t.Fatalf("unsafe guardrail response: status=%d body=%s", response.Code, body)
	}
	if rates.key != "admin-credential" || rates.tokens <= 0 {
		t.Fatalf("guardrail rate limit was not charged: %+v", rates)
	}
	if dlp.seen != "sensitive fixture" || av.seen != "sensitive fixture" {
		t.Fatalf("guardrail modules did not receive input: dlp=%q av=%q", dlp.seen, av.seen)
	}
	if len(audit.events) != 2 || audit.events[0].Action != "guardrail.apply" || audit.events[0].Outcome != "attempted" || audit.events[1].Outcome != "succeeded" || audit.events[1].Details["allowed"] != false {
		t.Fatalf("guardrail audit is incomplete: %+v", audit.events)
	}
	snapshot := monitor.Snapshot(10)
	if snapshot.Summary.Total != 2 || snapshot.Summary.Rejected != 1 || snapshot.Events[0].Source != "guardrail_api" || snapshot.Events[0].Policy != "strict" {
		t.Fatalf("guardrail execution was not monitored: %+v", snapshot)
	}
}

func TestApplyGuardrailRejectsUnattachedPolicyBeforeScannerAndAudit(t *testing.T) {
	runtime := provider.New(provider.Config{})
	_, _ = runtime.(provider.GuardrailController).UpdateGuardrailPolicy("strict", provider.GuardrailPolicy{DLP: true, Enabled: true})
	dlp := &complianceModule{name: "dlp"}
	audit := &recordingAuditClient{}
	handler := NewHandler(modulesPipeline("inference"), runtime).WithComplianceModules(dlp, &complianceModule{name: "av"}).WithAccessRegistry(NewAccessRegistry()).WithAudit(audit)

	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/guardrails/apply_guardrail", strings.NewReader(`{"guardrail_name":"strict","text":"fixture"}`)))
	if response.Code != http.StatusForbidden || dlp.seen != "" || len(audit.events) != 0 {
		t.Fatalf("unattached policy executed: status=%d seen=%q audit=%+v body=%s", response.Code, dlp.seen, audit.events, response.Body.String())
	}
}

func TestApplyGuardrailRequiresAuthentication(t *testing.T) {
	handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{err: modules.ErrUnauthorized}}), provider.New(provider.Config{}))
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/guardrails/apply_guardrail", strings.NewReader(`{"guardrail_name":"strict","text":"fixture"}`)))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestApplyGuardrailRejectsInvalidInputBeforeAuthentication(t *testing.T) {
	for _, body := range []string{
		`{"guardrail_name":"invalid name","text":"fixture"}`,
		`{"guardrail_name":"strict","text":"fixture","unknown":true}`,
	} {
		handler := NewHandler(modules.NewPipeline([]modules.Module{managementAuthModule{err: errors.New("authentication should not run")}}), provider.New(provider.Config{}))
		response := httptest.NewRecorder()
		Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/guardrails/apply_guardrail", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid input was accepted: status=%d body=%s", response.Code, response.Body.String())
		}
	}
}

func TestApplyGuardrailHonorsCredentialRateLimitBeforeAuditAndScanner(t *testing.T) {
	runtime := provider.New(provider.Config{})
	_, _ = runtime.(provider.GuardrailController).UpdateGuardrailPolicy("strict", provider.GuardrailPolicy{DLP: true, Enabled: true})
	dlp := &complianceModule{name: "dlp"}
	audit := &recordingAuditClient{}
	rates := &guardrailRateStore{allowed: false}
	handler := NewHandlerWithRateLimitStore(modulesPipeline("admin"), runtime, rates).WithComplianceModules(dlp, &complianceModule{name: "av"}).WithAudit(audit)

	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/guardrails/apply_guardrail", strings.NewReader(`{"guardrail_name":"strict","text":"fixture"}`)))
	if response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" || dlp.seen != "" || len(audit.events) != 0 {
		t.Fatalf("rate-limited guardrail executed: status=%d seen=%q audit=%+v", response.Code, dlp.seen, audit.events)
	}
}

func TestApplyGuardrailFailsClosedOnAuditAndScannerOutages(t *testing.T) {
	for _, test := range []struct {
		name       string
		audit      *recordingAuditClient
		dlp        modules.Module
		wantEvents int
	}{
		{name: "audit", audit: &recordingAuditClient{appendErr: errors.New("postgres unavailable")}, dlp: &complianceModule{name: "dlp"}},
		{name: "scanner", audit: &recordingAuditClient{}, dlp: &complianceErrorModule{}, wantEvents: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := provider.New(provider.Config{})
			_, _ = runtime.(provider.GuardrailController).UpdateGuardrailPolicy("strict", provider.GuardrailPolicy{DLP: true, Enabled: true})
			handler := NewHandler(modulesPipeline("admin"), runtime).WithComplianceModules(test.dlp, &complianceModule{name: "av"}).WithAudit(test.audit)
			response := httptest.NewRecorder()
			Routes(handler).ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/guardrails/apply_guardrail", strings.NewReader(`{"guardrail_name":"strict","text":"fixture"}`)))
			if response.Code != http.StatusServiceUnavailable || len(test.audit.events) != test.wantEvents || strings.Contains(response.Body.String(), "fixture") {
				t.Fatalf("outage did not fail closed: status=%d audit=%+v body=%s", response.Code, test.audit.events, response.Body.String())
			}
		})
	}
}

type complianceErrorModule struct{}

func (*complianceErrorModule) Name() string   { return "dlp" }
func (*complianceErrorModule) Required() bool { return true }
func (*complianceErrorModule) Handle(context.Context, *modules.RequestContext) error {
	return errors.New("scanner unavailable")
}
