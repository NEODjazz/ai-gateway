package provider

import (
	"testing"

	"ai-gateway-gateway/internal/config"
)

func TestRuntimeGuardrailPolicyChangesEndpointChecks(t *testing.T) {
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "endpoint", Type: "demo", Models: []string{"m"}, GuardrailPolicy: "strict"}}}).(*Router)
	if endpoint := router.runtimeEndpoints()[0]; endpoint.GuardrailPolicyValid {
		t.Fatalf("unknown policy unexpectedly valid: %+v", endpoint)
	}
	policy, err := router.UpdateGuardrailPolicy("strict", GuardrailPolicy{Description: "DLP and AV", DLP: true, AV: true, Enabled: true})
	if err != nil || policy.Name != "strict" {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	endpoint := router.runtimeEndpoints()[0]
	if !endpoint.GuardrailPolicyValid || !endpoint.DLPEnabled || !endpoint.AVEnabled {
		t.Fatalf("runtime policy not applied: %+v", endpoint)
	}
	if policies := router.ListGuardrailPolicies(); len(policies) != 1 || policies[0].Description != "DLP and AV" {
		t.Fatalf("policies=%+v", policies)
	}
}
