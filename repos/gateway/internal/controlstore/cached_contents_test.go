package controlstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/cachedstate"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

func TestPostgresCachedContentOwnershipExpiryQuotaAndPaginationIntegration(t *testing.T) {
	dsn := requiredPostgresTestDSN(t)
	store, err := NewPostgresStore(t.Context(), dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	_, err = store.pool.Exec(t.Context(), `CREATE TABLE gateway_cached_contents (cached_content_name TEXT NOT NULL,owner_key TEXT NOT NULL,endpoint TEXT NOT NULL,model TEXT NOT NULL,deployment TEXT NOT NULL,policy_fingerprint TEXT NOT NULL,snapshot JSONB NOT NULL,expires_at TIMESTAMPTZ NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,cached_content_name))`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	record := cachedstate.Record{
		OwnerKey: "owner-a",
		Binding:  provider.CachedContentBinding{Endpoint: "gemini-primary", Model: "public-model", Deployment: strings.Repeat("a", 64), Policy: strings.Repeat("p", 64)},
		Content: openai.GeminiCachedContent{
			Name: "cachedContents/cache-a", DisplayName: "reference", Model: "models/gemini-test",
			CreateTime: now.Format(time.RFC3339Nano), UpdateTime: now.Format(time.RFC3339Nano), ExpireTime: now.Add(time.Hour).Format(time.RFC3339Nano),
			UsageMetadata: &openai.GeminiCachedContentUsage{TotalTokenCount: 4096},
		},
	}
	created, err := store.CreateCachedContentRecord(t.Context(), record, 1)
	if err != nil || created.CreatedAt.IsZero() || created.Binding != record.Binding {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	if _, err = store.GetCachedContentRecord(t.Context(), "owner-b", record.Content.Name); !errors.Is(err, cachedstate.ErrNotFound) {
		t.Fatalf("cross-owner get err=%v", err)
	}
	second := record
	second.Content.Name = "cachedContents/cache-b"
	if _, err = store.CreateCachedContentRecord(t.Context(), second, 1); !errors.Is(err, cachedstate.ErrQuotaExceeded) {
		t.Fatalf("quota err=%v", err)
	}
	updated := created.Content
	updated.UpdateTime = now.Add(time.Minute).Format(time.RFC3339Nano)
	updated.ExpireTime = now.Add(2 * time.Hour).Format(time.RFC3339Nano)
	if saved, updateErr := store.UpdateCachedContentRecord(t.Context(), record.OwnerKey, updated); updateErr != nil || !saved.ExpiresAt.Equal(now.Add(2*time.Hour)) || saved.Binding != record.Binding {
		t.Fatalf("updated=%+v err=%v", saved, updateErr)
	}
	if _, err = store.pool.Exec(t.Context(), `UPDATE gateway_cached_contents SET expires_at=now()-interval '1 second' WHERE owner_key=$1 AND cached_content_name=$2`, record.OwnerKey, record.Content.Name); err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetCachedContentRecord(t.Context(), record.OwnerKey, record.Content.Name); !errors.Is(err, cachedstate.ErrNotFound) {
		t.Fatalf("expired get err=%v", err)
	}
	if _, err = store.CreateCachedContentRecord(t.Context(), second, 1); err != nil {
		t.Fatalf("expired record consumed quota: %v", err)
	}
	third := record
	third.OwnerKey = "owner-page"
	third.Content.Name = "cachedContents/page-a"
	if _, err = store.CreateCachedContentRecord(t.Context(), third, 2); err != nil {
		t.Fatal(err)
	}
	third.Content.Name = "cachedContents/page-b"
	if _, err = store.CreateCachedContentRecord(t.Context(), third, 2); err != nil {
		t.Fatal(err)
	}
	page, next, err := store.ListCachedContentRecords(t.Context(), third.OwnerKey, 1, "")
	if err != nil || len(page) != 1 || next == "" {
		t.Fatalf("page=%+v next=%q err=%v", page, next, err)
	}
	last, lastNext, err := store.ListCachedContentRecords(t.Context(), third.OwnerKey, 1, next)
	if err != nil || len(last) != 1 || lastNext != "" || last[0].Content.Name == page[0].Content.Name {
		t.Fatalf("last=%+v next=%q err=%v", last, lastNext, err)
	}
	if err = store.DeleteCachedContentRecord(t.Context(), "owner-b", second.Content.Name); !errors.Is(err, cachedstate.ErrNotFound) {
		t.Fatalf("cross-owner delete err=%v", err)
	}
	if err = store.DeleteCachedContentRecord(t.Context(), second.OwnerKey, second.Content.Name); err != nil {
		t.Fatal(err)
	}
}

func TestCachedContentRecordPayloadRejectsInvalidMetadata(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	valid := cachedstate.Record{
		OwnerKey: "owner",
		Binding:  provider.CachedContentBinding{Endpoint: "gemini", Model: "public-model", Deployment: strings.Repeat("a", 64), Policy: strings.Repeat("p", 64)},
		Content: openai.GeminiCachedContent{
			Name: "cachedContents/cache", Model: "models/gemini-test", CreateTime: now.Format(time.RFC3339Nano),
			UpdateTime: now.Format(time.RFC3339Nano), ExpireTime: now.Add(time.Hour).Format(time.RFC3339Nano),
		},
	}
	invalid := []cachedstate.Record{
		func() cachedstate.Record { value := valid; value.OwnerKey = ""; return value }(),
		func() cachedstate.Record { value := valid; value.Binding.Deployment = "short"; return value }(),
		func() cachedstate.Record { value := valid; value.Binding.Policy = "short"; return value }(),
		func() cachedstate.Record { value := valid; value.Content.Name = "cachedContents/a/b"; return value }(),
		func() cachedstate.Record { value := valid; value.Content.Model = "gemini-test"; return value }(),
		func() cachedstate.Record { value := valid; value.Content.ExpireTime = "later"; return value }(),
		func() cachedstate.Record { value := valid; value.ExpiresAt = now.Add(2 * time.Hour); return value }(),
	}
	for _, record := range invalid {
		if _, _, err := cachedContentRecordPayload(record); !errors.Is(err, cachedstate.ErrInvalid) {
			t.Fatalf("invalid record accepted: %+v err=%v", record, err)
		}
	}
}
