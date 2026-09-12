package provider

import (
	"reflect"
	"testing"

	"ai-gateway-gateway/internal/config"
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
}

func TestOutputDLPRequiresInputDLP(t *testing.T) {
	router := New(Config{}).(*Router)
	if _, err := router.UpdateGuardrailPolicy("invalid", GuardrailPolicy{OutputDLP: true, AV: true, Enabled: true}); err == nil {
		t.Fatal("output DLP policy without DLP was accepted")
	}
}
