package main

import (
	"strings"
	"testing"
)

func TestBillingProviderMetadataExcludesRawError(t *testing.T) {
	const privateError = "private prompt and credential"
	metadata := billingProviderMetadata(usageRequest{
		ProviderID:   "provider-1",
		Status:       "error",
		Error:        privateError,
		FailureClass: "upstream",
	})
	if metadata["provider.id"] != "provider-1" || metadata["provider.status"] != "error" || metadata["provider.failure_class"] != "upstream" {
		t.Fatalf("safe failure metadata missing: %+v", metadata)
	}
	if _, exists := metadata["provider.error"]; exists {
		t.Fatal("raw provider error included in billing metadata")
	}
	for _, value := range metadata {
		if strings.Contains(value, privateError) {
			t.Fatal("raw provider error leaked through another billing metadata field")
		}
	}
}
