package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func awsTestEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestAWSCredentialSourceUsesAndCachesContainerCredentials(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer workload-token" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = fmt.Fprintf(w, `{"AccessKeyId":"AKID","SecretAccessKey":"secret","Token":"session","Expiration":%q}`, now.Add(time.Hour).Format(time.RFC3339))
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("Bearer workload-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := newAWSCredentialSource("")
	source.now = func() time.Time { return now }
	source.getenv = awsTestEnvironment(map[string]string{
		"AWS_CONTAINER_CREDENTIALS_FULL_URI":     server.URL + "/credentials",
		"AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
	})

	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			credential, err := source.Credential(t.Context())
			if err != nil || credential.AccessKeyID != "AKID" || credential.SessionToken != "session" {
				t.Errorf("credential=%+v err=%v", credential, err)
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("container credential calls=%d", calls.Load())
	}
}

func TestAWSCredentialSourceUsesIMDSv2(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/api/token":
			if r.Method != http.MethodPut || r.Header.Get("X-aws-ec2-metadata-token-ttl-seconds") != "21600" {
				t.Fatalf("invalid token request: %s %v", r.Method, r.Header)
			}
			_, _ = fmt.Fprint(w, "metadata-token")
		case "/latest/meta-data/iam/security-credentials/":
			if r.Header.Get("X-aws-ec2-metadata-token") != "metadata-token" {
				t.Fatalf("missing metadata token: %v", r.Header)
			}
			_, _ = fmt.Fprint(w, "gateway-role")
		case "/latest/meta-data/iam/security-credentials/gateway-role":
			if r.Header.Get("X-aws-ec2-metadata-token") != "metadata-token" {
				t.Fatalf("missing metadata token: %v", r.Header)
			}
			_, _ = fmt.Fprintf(w, `{"AccessKeyId":"IMDSKEY","SecretAccessKey":"secret","Token":"session","Expiration":%q}`, now.Add(time.Hour).Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	source := newAWSCredentialSource("")
	source.now = func() time.Time { return now }
	source.getenv = awsTestEnvironment(nil)
	source.imdsBaseURL = server.URL
	credential, err := source.Credential(context.Background())
	if err != nil || credential.AccessKeyID != "IMDSKEY" || credential.SessionToken != "session" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

func TestAWSCredentialSourceRejectsUnsafeContainerEndpoint(t *testing.T) {
	source := newAWSCredentialSource("")
	source.getenv = awsTestEnvironment(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://example.com/credentials"})
	if _, err := source.Credential(t.Context()); err == nil {
		t.Fatal("unsafe container credential endpoint accepted")
	}
}

func TestAWSCredentialSourceRejectsPartialEnvironment(t *testing.T) {
	source := newAWSCredentialSource("")
	source.getenv = awsTestEnvironment(map[string]string{"AWS_ACCESS_KEY_ID": "AKID"})
	if _, err := source.Credential(t.Context()); err == nil {
		t.Fatal("partial environment credential accepted")
	}
}

func TestAWSCredentialSourceCoalescesContainerFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	source := newAWSCredentialSource("")
	source.getenv = awsTestEnvironment(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": server.URL})
	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			if _, err := source.Credential(t.Context()); err == nil {
				t.Error("failed container credential request succeeded")
			}
		}()
	}
	wait.Wait()
	if calls.Load() != 1 {
		t.Fatalf("container credential calls=%d", calls.Load())
	}
}

func TestAWSCredentialSourceUsesCachedCredentialUntilExpiration(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := base
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) > 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprintf(w, `{"AccessKeyId":"AKID","SecretAccessKey":"secret","Token":"session","Expiration":%q}`, base.Add(10*time.Minute).Format(time.RFC3339))
	}))
	defer server.Close()
	source := newAWSCredentialSource("")
	source.now = func() time.Time { return now }
	source.getenv = awsTestEnvironment(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": server.URL})
	if _, err := source.Credential(t.Context()); err != nil {
		t.Fatal(err)
	}
	now = base.Add(6 * time.Minute)
	if credential, err := source.Credential(t.Context()); err != nil || credential.AccessKeyID != "AKID" {
		t.Fatalf("cached credential=%+v err=%v", credential, err)
	}
	now = base.Add(11 * time.Minute)
	if _, err := source.Credential(t.Context()); err == nil {
		t.Fatal("expired cached credential accepted")
	}
	if calls.Load() != 3 {
		t.Fatalf("container credential calls=%d", calls.Load())
	}
}

func TestAWSContainerRelativeURIUsesFixedEndpoint(t *testing.T) {
	endpoint, err := awsContainerRelativeURL(awsECSCredentialBaseURL, "/v2/credentials?id=one")
	if err != nil || endpoint != "http://169.254.170.2/v2/credentials?id=one" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
	for _, value := range []string{"//example.com/credentials", "/../credentials", "http://example.com/credentials"} {
		if _, err := awsContainerRelativeURL(awsECSCredentialBaseURL, value); err == nil {
			t.Fatalf("unsafe relative URI accepted: %q", value)
		}
	}
}
