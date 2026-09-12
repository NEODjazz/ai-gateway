package provider

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAzureTokenSourceUsesAndCachesFederatedWorkloadIdentity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tokenFile := filepath.Join(t.TempDir(), "federated-token")
	if err := os.WriteFile(tokenFile, []byte("projected.jwt\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/tenant-id/oauth2/v2.0/token" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected token request: %s %s headers=%v", r.Method, r.URL.Path, r.Header)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		want := map[string]string{
			"client_id": "client-id", "scope": azureOpenAIScope,
			"client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
			"client_assertion":      "projected.jwt", "grant_type": "client_credentials",
		}
		for name, value := range want {
			if r.Form.Get(name) != value {
				t.Errorf("%s=%q", name, r.Form.Get(name))
			}
		}
		_, _ = fmt.Fprint(w, `{"access_token":"federated-access-token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	source := newAzureTokenSource("")
	source.now = func() time.Time { return now }
	source.authorityBaseURL = server.URL
	source.getenv = awsTestEnvironment(map[string]string{
		"AZURE_TENANT_ID": "tenant-id", "AZURE_CLIENT_ID": "client-id", "AZURE_FEDERATED_TOKEN_FILE": tokenFile,
		"IDENTITY_ENDPOINT": "http://127.0.0.1:1", "IDENTITY_HEADER": "must-not-be-used",
	})
	for range 2 {
		if token, err := source.Token(t.Context()); err != nil || token != "federated-access-token" {
			t.Fatalf("token=%q err=%v", token, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("token calls=%d", calls.Load())
	}
}

func TestAzureIdentityEndpointsSelectSupportedCloud(t *testing.T) {
	for _, test := range []struct {
		baseURL   string
		authority string
		resource  string
	}{
		{baseURL: "https://resource.openai.azure.com", authority: azureAuthorityURL, resource: azureOpenAIResource},
		{baseURL: "https://resource.openai.azure.us/openai/v1", authority: azureGovernmentAuthority, resource: azureGovernmentResource},
		{baseURL: "https://resource.cognitiveservices.azure.us", authority: azureGovernmentAuthority, resource: azureGovernmentResource},
		{baseURL: "https://resource.openai.azure.cn/openai/v1", authority: azureChinaAuthority, resource: azureChinaResource},
		{baseURL: "https://resource.cognitiveservices.azure.cn", authority: azureChinaAuthority, resource: azureChinaResource},
		{baseURL: "https://resource.openai.azure.cn.example.test", authority: azureAuthorityURL, resource: azureOpenAIResource},
		{baseURL: "https://custom.example.test", authority: azureAuthorityURL, resource: azureOpenAIResource},
	} {
		authority, resource := azureIdentityEndpoints(test.baseURL)
		if authority != test.authority || resource != test.resource {
			t.Fatalf("base_url=%q authority=%q resource=%q", test.baseURL, authority, resource)
		}
	}
}

func TestAzureChinaWorkloadIdentityUsesSovereignAudience(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("assertion"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/oauth2/v2.0/token" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("scope") != azureChinaResource+".default" {
			t.Errorf("scope=%q", r.Form.Get("scope"))
		}
		_, _ = fmt.Fprint(w, `{"access_token":"token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	source := newAzureTokenSource("", "https://resource.openai.azure.cn")
	source.now = func() time.Time { return now }
	source.authorityBaseURL = server.URL
	source.getenv = awsTestEnvironment(map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_FEDERATED_TOKEN_FILE": tokenFile})
	if _, err := source.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAzureChinaManagedIdentityUsesSovereignResource(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") != azureChinaResource {
			t.Errorf("resource=%q", r.URL.Query().Get("resource"))
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"token","expires_on":%d,"token_type":"Bearer"}`, now.Add(time.Hour).Unix())
	}))
	defer server.Close()
	source := newAzureTokenSource("", "https://resource.cognitiveservices.azure.cn")
	source.now = func() time.Time { return now }
	source.imdsURL = server.URL
	source.getenv = awsTestEnvironment(nil)
	if _, err := source.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAzureGovernmentWorkloadIdentityUsesSovereignAudience(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("assertion"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tenant/oauth2/v2.0/token" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		if r.Form.Get("scope") != azureGovernmentResource+".default" {
			t.Errorf("scope=%q", r.Form.Get("scope"))
		}
		_, _ = fmt.Fprint(w, `{"access_token":"token","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer server.Close()
	source := newAzureTokenSource("", "https://resource.openai.azure.us")
	source.now = func() time.Time { return now }
	source.authorityBaseURL = server.URL
	source.getenv = awsTestEnvironment(map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_FEDERATED_TOKEN_FILE": tokenFile})
	if _, err := source.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAzureGovernmentManagedIdentityUsesSovereignResource(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("resource") != azureGovernmentResource {
			t.Errorf("resource=%q", r.URL.Query().Get("resource"))
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"token","expires_on":%d,"token_type":"Bearer"}`, now.Add(time.Hour).Unix())
	}))
	defer server.Close()
	source := newAzureTokenSource("", "https://resource.cognitiveservices.azure.us")
	source.now = func() time.Time { return now }
	source.imdsURL = server.URL
	source.getenv = awsTestEnvironment(nil)
	if _, err := source.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAzureTokenSourceRejectsInvalidFederatedConfiguration(t *testing.T) {
	oversized := filepath.Join(t.TempDir(), "oversized")
	if err := os.WriteFile(oversized, make([]byte, azureAssertionMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string]map[string]string{
		"partial":         {"AZURE_TENANT_ID": "tenant-id", "AZURE_CLIENT_ID": "client-id"},
		"relative file":   {"AZURE_TENANT_ID": "tenant-id", "AZURE_CLIENT_ID": "client-id", "AZURE_FEDERATED_TOKEN_FILE": "token"},
		"oversized token": {"AZURE_TENANT_ID": "tenant-id", "AZURE_CLIENT_ID": "client-id", "AZURE_FEDERATED_TOKEN_FILE": oversized},
		"unsafe tenant":   {"AZURE_TENANT_ID": "../tenant", "AZURE_CLIENT_ID": "client-id", "AZURE_FEDERATED_TOKEN_FILE": oversized},
	} {
		t.Run(name, func(t *testing.T) {
			source := newAzureTokenSource("")
			source.getenv = awsTestEnvironment(values)
			if _, err := source.Token(t.Context()); err == nil {
				t.Fatal("invalid federated configuration accepted")
			}
		})
	}
}

func TestAzureTokenSourceRejectsInvalidFederatedResponses(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("assertion"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "authority failure", status: http.StatusServiceUnavailable, body: "sensitive error"},
		{name: "malformed response", status: http.StatusOK, body: "{"},
		{name: "excessive lifetime", status: http.StatusOK, body: `{"access_token":"token","expires_in":90000,"token_type":"Bearer"}`},
		{name: "oversized response", status: http.StatusOK, body: string(make([]byte, azureTokenMaxBytes+1))},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			source := newAzureTokenSource("")
			source.authorityBaseURL = server.URL
			source.getenv = awsTestEnvironment(map[string]string{"AZURE_TENANT_ID": "tenant", "AZURE_CLIENT_ID": "client", "AZURE_FEDERATED_TOKEN_FILE": tokenFile})
			if _, err := source.Token(t.Context()); err == nil {
				t.Fatal("invalid federated response accepted")
			}
		})
	}
}

func TestAzureTokenSourceUsesCachedTokenUntilExpiration(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := base
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) > 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, `{"access_token":"cached-token","expires_on":%d,"token_type":"Bearer"}`, base.Add(10*time.Minute).Unix())
	}))
	defer server.Close()
	source := newAzureTokenSource("")
	source.now = func() time.Time { return now }
	source.getenv = awsTestEnvironment(nil)
	source.imdsURL = server.URL
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
		t.Fatalf("token calls=%d", calls.Load())
	}
}

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
