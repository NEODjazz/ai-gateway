package main

import (
	"testing"

	"ai-gateway-gateway/internal/controlstore"
	"ai-gateway-gateway/internal/redisstore"
)

func TestRegistryStoreForDoesNotReturnTypedNil(t *testing.T) {
	var store *redisstore.Store
	if registryStoreFor(store) != nil {
		t.Fatal("nil Redis pointer must remain a nil registry interface")
	}
}

func TestControlPlaneStoreForDoesNotReturnTypedNil(t *testing.T) {
	var store *controlstore.PostgresStore
	if controlPlaneStoreFor(store) != nil {
		t.Fatal("nil PostgreSQL pointer must remain a nil control-plane interface")
	}
}
