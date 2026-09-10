package provider

import (
	"context"
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

func awsTestEnvironment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestAWSCredentialSourceUsesSharedCredentialsProfile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "credentials")
	data := `[default]
aws_access_key_id = DEFAULT
aws_secret_access_key = default-secret

[production]
aws_access_key_id = PROFILEKEY
aws_secret_access_key = profile-secret
aws_session_token = profile-session
region = us-east-1
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	source := newAWSCredentialSource("", "us-east-1")
	source.getenv = awsTestEnvironment(map[string]string{"AWS_SHARED_CREDENTIALS_FILE": path, "AWS_PROFILE": "production"})
	credential, err := source.Credential(t.Context())
	if err != nil || credential.AccessKeyID != "PROFILEKEY" || credential.SecretAccessKey != "profile-secret" || credential.SessionToken != "profile-session" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

func TestAWSCredentialSourceUsesDefaultHomeProfile(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".aws"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aws", "credentials"), []byte("[default]\naws_access_key_id=HOMEKEY\naws_secret_access_key=home-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := newAWSCredentialSource("", "us-east-1")
	source.getenv = awsTestEnvironment(map[string]string{"HOME": home})
	credential, err := source.Credential(t.Context())
	if err != nil || credential.AccessKeyID != "HOMEKEY" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

func TestAWSCredentialSourceEnvironmentPrecedesSharedProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte("[default]\naws_access_key_id=FILEKEY\naws_secret_access_key=file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := newAWSCredentialSource("", "us-east-1")
	source.getenv = awsTestEnvironment(map[string]string{
		"AWS_ACCESS_KEY_ID": "ENVKEY", "AWS_SECRET_ACCESS_KEY": "env-secret", "AWS_SHARED_CREDENTIALS_FILE": path,
	})
	credential, err := source.Credential(t.Context())
	if err != nil || credential.AccessKeyID != "ENVKEY" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

func TestAWSCredentialSourceRejectsInvalidSharedCredentials(t *testing.T) {
	directory := t.TempDir()
	files := map[string]string{
		"missing-key":       "[default]\naws_access_key_id=KEY\n",
		"duplicate-key":     "[default]\naws_access_key_id=ONE\naws_access_key_id=TWO\naws_secret_access_key=secret\n",
		"duplicate-profile": "[default]\naws_access_key_id=ONE\naws_secret_access_key=secret\n[default]\naws_access_key_id=TWO\naws_secret_access_key=secret\n",
	}
	for name, data := range files {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		source := newAWSCredentialSource("", "us-east-1")
		source.getenv = awsTestEnvironment(map[string]string{"AWS_SHARED_CREDENTIALS_FILE": path})
		if _, err := source.Credential(t.Context()); err == nil {
			t.Fatalf("%s credentials accepted", name)
		}
	}
	oversized := filepath.Join(directory, "oversized")
	if err := os.WriteFile(oversized, make([]byte, awsSharedFileMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultOnly := filepath.Join(directory, "default-only")
	if err := os.WriteFile(defaultOnly, []byte("[default]\naws_access_key_id=KEY\naws_secret_access_key=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, values := range map[string]map[string]string{
		"relative path":   {"AWS_SHARED_CREDENTIALS_FILE": "credentials"},
		"missing file":    {"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(directory, "missing")},
		"missing profile": {"AWS_SHARED_CREDENTIALS_FILE": defaultOnly, "AWS_PROFILE": "production"},
		"invalid profile": {"AWS_SHARED_CREDENTIALS_FILE": oversized, "AWS_PROFILE": "bad]profile"},
		"oversized file":  {"AWS_SHARED_CREDENTIALS_FILE": oversized},
	} {
		source := newAWSCredentialSource("", "us-east-1")
		source.getenv = awsTestEnvironment(values)
		if _, err := source.Credential(t.Context()); err == nil {
			t.Fatalf("%s configuration accepted", name)
		}
	}
}

func TestAWSCredentialSourceUsesAndCachesWebIdentity(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("projected.jwt.token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected request: %s %s content-type=%q", r.Method, r.URL.Path, r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		want := map[string]string{
			"Action":           "AssumeRoleWithWebIdentity",
			"Version":          "2011-06-15",
			"RoleArn":          "arn:aws:iam::123456789012:role/gateway",
			"RoleSessionName":  "gateway-session",
			"WebIdentityToken": "projected.jwt.token",
		}
		for name, value := range want {
			if r.Form.Get(name) != value {
				t.Errorf("%s=%q", name, r.Form.Get(name))
			}
		}
		_, _ = fmt.Fprintf(w, `<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>WEBKEY</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, now.Add(time.Hour).Format(time.RFC3339))
	}))
	defer server.Close()
	source := newAWSCredentialSource("", "us-east-1")
	source.now = func() time.Time { return now }
	source.stsBaseURL = server.URL
	source.getenv = awsTestEnvironment(map[string]string{
		"AWS_ROLE_ARN":                "arn:aws:iam::123456789012:role/gateway",
		"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
		"AWS_ROLE_SESSION_NAME":       "gateway-session",
	})
	for range 2 {
		credential, err := source.Credential(t.Context())
		if err != nil || credential.AccessKeyID != "WEBKEY" || credential.SessionToken != "session" {
			t.Fatalf("credential=%+v err=%v", credential, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("web identity calls=%d", calls.Load())
	}
}

func TestAWSCredentialSourceRejectsInvalidWebIdentityConfiguration(t *testing.T) {
	oversizedFile := filepath.Join(t.TempDir(), "oversized-token")
	if err := os.WriteFile(oversizedFile, make([]byte, awsWebIdentityMaxBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		values map[string]string
	}{
		{name: "missing token file", values: map[string]string{"AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/gateway"}},
		{name: "relative token file", values: map[string]string{"AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/gateway", "AWS_WEB_IDENTITY_TOKEN_FILE": "token"}},
		{name: "oversized token", values: map[string]string{"AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/gateway", "AWS_WEB_IDENTITY_TOKEN_FILE": oversizedFile}},
		{name: "invalid role session", values: map[string]string{"AWS_ROLE_ARN": "arn:aws:iam::123456789012:role/gateway", "AWS_WEB_IDENTITY_TOKEN_FILE": oversizedFile, "AWS_ROLE_SESSION_NAME": "bad session"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := newAWSCredentialSource("", "us-east-1")
			source.getenv = awsTestEnvironment(test.values)
			source.imdsBaseURL = "http://127.0.0.1:1"
			if _, err := source.Credential(t.Context()); err == nil {
				t.Fatal("invalid web identity configuration accepted")
			}
		})
	}
}

func TestAWSCredentialSourceRejectsInvalidWebIdentityResponses(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("token"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "server failure", status: http.StatusServiceUnavailable, body: "credential must not be exposed"},
		{name: "malformed XML", status: http.StatusOK, body: "<response>"},
		{name: "expired credentials", status: http.StatusOK, body: fmt.Sprintf(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>WEBKEY</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, now.Add(-time.Minute).Format(time.RFC3339))},
		{name: "oversized response", status: http.StatusOK, body: string(make([]byte, awsCredentialMaxBytes+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			source := newAWSCredentialSource("", "us-east-1")
			source.now = func() time.Time { return now }
			source.stsBaseURL = server.URL
			source.getenv = awsTestEnvironment(map[string]string{
				"AWS_ROLE_ARN":                "arn:aws:iam::123456789012:role/gateway",
				"AWS_WEB_IDENTITY_TOKEN_FILE": tokenFile,
			})
			if _, err := source.Credential(t.Context()); err == nil {
				t.Fatal("invalid web identity response accepted")
			}
		})
	}
}

func TestAWSSTSEndpointUsesRegionalPartition(t *testing.T) {
	if endpoint := awsSTSEndpoint("us-east-1"); endpoint != "https://sts.us-east-1.amazonaws.com" {
		t.Fatalf("endpoint=%q", endpoint)
	}
	if endpoint := awsSTSEndpoint("cn-north-1"); endpoint != "https://sts.cn-north-1.amazonaws.com.cn" {
		t.Fatalf("endpoint=%q", endpoint)
	}
}

func TestAWSManagedCredentialSourceChangesWithRegion(t *testing.T) {
	router := Router{awsCredentials: &awsCredentialRegistry{current: make(map[string]managedAWSCredentialSource)}}
	first := router.awsCredentialSource("provider", "credential", "", "us-east-1")
	if same := router.awsCredentialSource("provider", "credential", "", "us-east-1"); same != first {
		t.Fatal("unchanged credential source was not reused")
	}
	changed := router.awsCredentialSource("provider", "credential", "", "us-west-2")
	if changed == first || changed.stsBaseURL != "https://sts.us-west-2.amazonaws.com" {
		t.Fatalf("region change reused source: %+v", changed)
	}
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
	source := newAWSCredentialSource("", "us-east-1")
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
	source := newAWSCredentialSource("", "us-east-1")
	source.now = func() time.Time { return now }
	source.getenv = awsTestEnvironment(nil)
	source.imdsBaseURL = server.URL
	credential, err := source.Credential(context.Background())
	if err != nil || credential.AccessKeyID != "IMDSKEY" || credential.SessionToken != "session" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
}

func TestAWSCredentialSourceRejectsUnsafeContainerEndpoint(t *testing.T) {
	source := newAWSCredentialSource("", "us-east-1")
	source.getenv = awsTestEnvironment(map[string]string{"AWS_CONTAINER_CREDENTIALS_FULL_URI": "http://example.com/credentials"})
	if _, err := source.Credential(t.Context()); err == nil {
		t.Fatal("unsafe container credential endpoint accepted")
	}
}

func TestAWSCredentialSourceRejectsPartialEnvironment(t *testing.T) {
	source := newAWSCredentialSource("", "us-east-1")
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
	source := newAWSCredentialSource("", "us-east-1")
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
	source := newAWSCredentialSource("", "us-east-1")
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
