package provider

import (
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"context"
	"fmt"
	"testing"
	"time"
)

func TestCacheIsolationIncludesIdentityAndEffectivePolicy(t *testing.T) {
	base := modules.RequestContext{CredentialID: "key", UserID: "user", TeamID: "team", AllowedModels: []string{"b", "a"}, Request: openai.ChatCompletionRequest{Model: "model", Messages: []openai.Message{{Role: "user", Content: "hello"}}}, Metadata: map[string]string{"policy.modules.dlp.enabled": "true"}}
	for _, tc := range []struct {
		name   string
		change func(*modules.RequestContext)
	}{
		{"credential", func(r *modules.RequestContext) { r.CredentialID = "other" }},
		{"user", func(r *modules.RequestContext) { r.UserID = "other" }},
		{"team", func(r *modules.RequestContext) { r.TeamID = "other" }},
		{"policy", func(r *modules.RequestContext) { r.Metadata = map[string]string{"policy.modules.dlp.enabled": "false"} }},
		{"grants", func(r *modules.RequestContext) { r.AllowedTools = []string{"new-tool"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.change(&changed)
			if providerCacheKey("chat", base) == providerCacheKey("chat", changed) {
				t.Fatal("exact scope shared")
			}
			a, _, _ := semanticRequest(base, Endpoint{Name: "endpoint"})
			b, _, _ := semanticRequest(changed, Endpoint{Name: "endpoint"})
			if a == b {
				t.Fatal("semantic scope shared")
			}
		})
	}
	same := base
	same.AllowedModels = []string{"a", "b"}
	same.RequestID = "another-execution"
	if providerCacheKey("chat", base) != providerCacheKey("chat", same) {
		t.Fatal("non-policy change invalidated cache")
	}
	noIdentity := base
	noIdentity.CredentialID = ""
	if providerCacheKey("chat", noIdentity) != "" {
		t.Fatal("anonymous cache enabled")
	}
	if affinityKey(base, "response") == affinityKey(modules.RequestContext{CredentialID: "other", UserID: base.UserID, TeamID: base.TeamID}, "response") {
		t.Fatal("affinity shared across credentials")
	}
}
func TestMemoryCacheAndAffinityBounded(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1, 0)
	c := newExactCache(time.Minute)
	c.now = func() time.Time { return now }
	a := newAffinityStore(time.Minute, nil).(*memoryAffinity)
	a.now = c.now
	for i := 0; i < 10000; i++ {
		key := fmt.Sprint(i)
		_ = c.set(ctx, key, []byte("value"))
		_ = a.set(ctx, key, "endpoint")
	}
	if n, b := c.entries.Size(); n > memoryCacheMaxEntries || b > memoryCacheMaxBytes {
		t.Fatal("cache overflow")
	}
	if n, b := a.entries.Size(); n > memoryAffinityMaxEntries || b > memoryAffinityMaxBytes {
		t.Fatal("affinity overflow")
	}
	now = now.Add(time.Hour)
	_ = c.set(ctx, "new", []byte("value"))
	_ = a.set(ctx, "new", "endpoint")
	if n, _ := c.entries.Size(); n != 1 {
		t.Fatal("expired cache retained")
	}
	if n, _ := a.entries.Size(); n != 1 {
		t.Fatal("expired affinity retained")
	}
}

func TestSemanticCacheHasGlobalByteLimit(t *testing.T) {
	c := newSemanticResponseCache(semanticCacheConfig{ttl: time.Minute, maxEntries: 10000, maxBytes: 1 << 20, embedder: &semanticTestEmbedder{}})
	if c.maxEntries != semanticCacheMaxEntries {
		t.Fatal("entry limit not clamped")
	}
	payload := make([]byte, 1<<20)
	for i := 0; i < 80; i++ {
		c.set(fmt.Sprint(i), []float64{1}, payload)
	}
	count, bytes := 0, 0
	for scope, entries := range c.entries {
		for _, entry := range entries {
			count++
			bytes += semanticEntryBytes(scope, entry.vector, entry.payload)
		}
	}
	if bytes > semanticCacheMaxTotalBytes || count >= 80 {
		t.Fatalf("unbounded semantic cache: %d entries, %d bytes", count, bytes)
	}
}
