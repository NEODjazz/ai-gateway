package provider

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGCPTokenSourceUsesAndCachesMetadataToken(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Metadata-Flavor") != "Google" {
			t.Errorf("unexpected metadata request: %s headers=%v", r.Method, r.Header)
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = fmt.Fprint(w, `{"access_token":"gcp-token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	source := newGCPTokenSource()
	source.now = func() time.Time { return base }
	source.metadataURL = server.URL
	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			if token, err := source.Token(t.Context()); err != nil || token != "gcp-token" {
				t.Errorf("token=%q err=%v", token, err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("metadata calls=%d", calls.Load())
	}
}

func TestGCPTokenSourceRejectsInvalidResponses(t *testing.T) {
	for _, body := range []string{
		`{}`, `{"access_token":"token","expires_in":0,"token_type":"Bearer"}`,
		`{"access_token":"token","expires_in":90000,"token_type":"Bearer"}`,
		`{"access_token":"token","expires_in":3600,"token_type":"Basic"}`,
		string(make([]byte, gcpTokenMaxBytes+1)),
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Metadata-Flavor", "Google")
			_, _ = fmt.Fprint(w, body)
		}))
		source := newGCPTokenSource()
		source.metadataURL = server.URL
		_, err := source.Token(t.Context())
		server.Close()
		if err == nil {
			t.Fatal("invalid metadata response accepted")
		}
	}
}

func TestGCPTokenSourceRequiresMetadataResponseHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"access_token":"token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	source := newGCPTokenSource()
	source.metadataURL = server.URL
	if _, err := source.Token(t.Context()); err == nil {
		t.Fatal("metadata response without Metadata-Flavor was accepted")
	}
}

func TestGCPTokenSourceUsesCachedTokenUntilExpiration(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := base
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) > 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Metadata-Flavor", "Google")
		_, _ = fmt.Fprint(w, `{"access_token":"cached-token","expires_in":600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	source := newGCPTokenSource()
	source.now = func() time.Time { return now }
	source.metadataURL = server.URL
	if _, err := source.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = base.Add(6 * time.Minute)
	if token, err := source.Token(t.Context()); err != nil || token != "cached-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	now = base.Add(11 * time.Minute)
	if _, err := source.Token(t.Context()); err == nil {
		t.Fatal("expired cached token accepted")
	}
	if calls.Load() != 3 {
		t.Fatalf("metadata calls=%d", calls.Load())
	}
}
