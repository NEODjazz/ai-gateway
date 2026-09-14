package provider

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestRuntimeGuardrailPolicyChangesEndpointChecks(t *testing.T) {
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "endpoint", Type: "demo", Models: []string{"m"}, GuardrailPolicy: "strict"}}}).(*Router)
	if endpoint := router.runtimeEndpoints()[0]; endpoint.GuardrailPolicyValid {
		t.Fatalf("unknown policy unexpectedly valid: %+v", endpoint)
	}
	policy, err := router.UpdateGuardrailPolicy("strict", GuardrailPolicy{Description: "DLP and AV", DLP: true, OutputDLP: true, AV: true, Enabled: true})
	if err != nil || policy.Name != "strict" {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	endpoint := router.runtimeEndpoints()[0]
	if !endpoint.GuardrailPolicyValid || !endpoint.DLPEnabled || !endpoint.OutputDLPEnabled || !endpoint.AVEnabled {
		t.Fatalf("runtime policy not applied: %+v", endpoint)
	}
	if policies := router.ListGuardrailPolicies(); len(policies) != 1 || policies[0].Description != "DLP and AV" {
		t.Fatalf("policies=%+v", policies)
	}
}

func TestAnonymizationPolicyValidationAndComposition(t *testing.T) {
	router := New(Config{}).(*Router)
	custom, err := router.UpdateGuardrailPolicy("customer-pii", GuardrailPolicy{Anonymization: "custom", AnonymizationRules: []string{"phone", "email", "phone"}, Enabled: true})
	if err != nil || !reflect.DeepEqual(custom.AnonymizationRules, []string{"email", "phone"}) {
		t.Fatalf("custom=%+v err=%v", custom, err)
	}
	if _, err := router.UpdateGuardrailPolicy("bad-custom", GuardrailPolicy{Anonymization: "custom", Enabled: true}); err == nil {
		t.Fatal("custom anonymization without rules was accepted")
	}
	if _, err := router.UpdateGuardrailPolicy("bad-strict", GuardrailPolicy{Anonymization: "strict", AnonymizationRules: []string{"email"}, Enabled: true}); err == nil {
		t.Fatal("strict anonymization with redundant rules was accepted")
	}
	if _, err := router.UpdateGuardrailPolicy("bad-rule", GuardrailPolicy{Anonymization: "custom", AnonymizationRules: []string{"email,phone"}, Enabled: true}); err == nil {
		t.Fatal("unsafe custom rule name was accepted")
	}

	mode, rules, profiles := ResolveAnonymization(
		AnonymizationSetting{Profile: "local", Mode: "disabled"},
		AnonymizationSetting{Profile: "customer-pii", Mode: "custom", Rules: []string{"phone", "email"}},
	)
	if mode != "custom" || !reflect.DeepEqual(rules, []string{"email", "phone"}) || !reflect.DeepEqual(profiles, []string{"customer-pii", "local"}) {
		t.Fatalf("mode=%q rules=%v profiles=%v", mode, rules, profiles)
	}
	mode, rules, _ = ResolveAnonymization(AnonymizationSetting{Mode: "disabled"}, AnonymizationSetting{Mode: "strict"})
	if mode != "strict" || len(rules) != 0 {
		t.Fatalf("strict did not win: mode=%q rules=%v", mode, rules)
	}
	mode, _, _ = ResolveAnonymization()
	if mode != "strict" {
		t.Fatalf("safe default=%q", mode)
	}

	mode, rules, _ = ResolveAnonymization(AnonymizationSetting{Mode: "custom", Rules: []string{"Person_Context", "inn_context", "password", "api_key"}})
	if mode != "custom" || !reflect.DeepEqual(rules, []string{"api_key", "inn", "person_ru", "secret"}) {
		t.Fatalf("legacy rule aliases were not normalized: mode=%q rules=%v", mode, rules)
	}
}

func TestOutputDLPRequiresInputDLP(t *testing.T) {
	router := New(Config{}).(*Router)
	if _, err := router.UpdateGuardrailPolicy("invalid", GuardrailPolicy{OutputDLP: true, AV: true, Enabled: true}); err == nil {
		t.Fatal("output DLP policy without DLP was accepted")
	}
}

func TestAnonymizationBasicRuleSetIsCompatibleWithBuiltinAnonymizer(t *testing.T) {
	mode, rules, _ := ResolveAnonymization(AnonymizationSetting{Mode: "basic"})
	if mode != "custom" || len(rules) == 0 {
		t.Fatalf("unexpected resolved basic anonymization: mode=%q rules=%v", mode, rules)
	}

	req := modules.RequestContext{
		Metadata: map[string]string{
			"provider.modules.anonymizer.mode":  mode,
			"provider.modules.anonymizer.rules": strings.Join(rules, ","),
		},
		Request: openai.ChatCompletionRequest{Messages: []openai.Message{{Role: "user", Content: "user@example.com password=qwerty123"}}},
	}

	module := modules.NewAnonymizerModule(true, rules...)
	if err := module.Handle(context.Background(), &req); err != nil {
		t.Fatalf("anonymizer failed to accept resolved basic rules: %v", err)
	}
	content := openai.ContentText(req.Request.Messages[0].Content)
	if !strings.Contains(content, "{{EMAIL_1}}") || !strings.Contains(content, "{{SECRET_1}}") {
		t.Fatalf("basic anonymization is not working for resolved rules: %q", content)
	}
}
