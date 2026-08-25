package main

import (
	"testing"

	"ai-gateway-gateway/internal/redisstore"
)

func TestRegistryStoreForDoesNotReturnTypedNil(t *testing.T) {
	var store *redisstore.Store
	if registryStoreFor(store) != nil {
		t.Fatal("nil Redis pointer must remain a nil registry interface")
	}
}
