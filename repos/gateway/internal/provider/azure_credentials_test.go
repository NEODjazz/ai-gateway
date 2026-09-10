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

func TestAzureTokenSourceUsesAndCachesIMDS(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Metadata") != "true" || r.URL.Query().Get("api-version") != "2018-02-01" || r.URL.Query().Get("resource") != azureOpenAIResource || r.URL.Query().Get("client_id") != "client-id" {
			t.Errorf("invalid request: headers=%v query=%v", r.Header, r.URL.Query())
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"managed-token","expires_on":%q,"token_type":"Bearer"}`, fmt.Sprint(now.Add(time.Hour).Unix()))
	}))
	defer server.Close()
	source := newAzureTokenSource("")
	source.now = func() time.Time { return now }
	source.getenv = awsTestEnvironment(map[string]string{"AZURE_CLIENT_ID": "client-id"})
	source.imdsURL = server.URL
	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			if token, err := source.Token(t.Context()); err != nil || token != "managed-token" {
				t.Errorf("token=%q err=%v", token, err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("token calls=%d", calls.Load())
	}
}

func TestAzureTokenSourceUsesAppServiceEndpoint(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-IDENTITY-HEADER") != "rotating-secret" || r.Header.Get("Metadata") != "" || r.URL.Query().Get("api-version") != "2019-08-01" {
			t.Errorf("invalid request: headers=%v query=%v", r.Header, r.URL.Query())
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"app-token","expires_on":%d,"token_type":"Bearer"}`, now.Add(time.Hour).Unix())
	}))
	defer server.Close()
	source := newAzureTokenSource("")
	source.now = func() time.Time { return now }
	source.getenv = awsTestEnvironment(map[string]string{"IDENTITY_ENDPOINT": server.URL, "IDENTITY_HEADER": "rotating-secret"})
	if token, err := source.Token(t.Context()); err != nil || token != "app-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
}

func TestAzureTokenSourceRejectsUnsafeOrInvalidResponses(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"partial endpoint": {"IDENTITY_ENDPOINT": "http://127.0.0.1/token"},
		"remote endpoint":  {"IDENTITY_ENDPOINT": "http://example.com/token", "IDENTITY_HEADER": "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			source := newAzureTokenSource("")
			source.getenv = awsTestEnvironment(values)
			if _, err := source.Token(t.Context()); err == nil {
				t.Fatal("unsafe identity configuration accepted")
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"access_token":"token","expires_on":"1","token_type":"Bearer"}`)
	}))
	defer server.Close()
	source := newAzureTokenSource("")
	source.imdsURL = server.URL
	source.getenv = awsTestEnvironment(nil)
	if _, err := source.Token(t.Context()); err == nil {
		t.Fatal("expired identity token accepted")
	}
}
