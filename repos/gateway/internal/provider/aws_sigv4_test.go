package provider

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSignAWSRequestMatchesCanonicalSignature(t *testing.T) {
	request, err := http.NewRequest(http.MethodPost, "https://bedrock-runtime.us-east-1.amazonaws.com/model/model/converse", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	credential := awsCredential{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", SessionToken: "token"}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := signAWSRequest(request, []byte(`{}`), credential, "us-east-1", "bedrock", now); err != nil {
		t.Fatal(err)
	}
	expected := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20260102/us-east-1/bedrock/aws4_request, SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date;x-amz-security-token, Signature=2511d767f5b57e0735d8ceffb85dea8e393a92892b9c4a2978d0cbdb0e3ecd3c"
	if request.Header.Get("Authorization") != expected || request.Header.Get("X-Amz-Date") != "20260102T030405Z" || request.Header.Get("X-Amz-Security-Token") != "token" || request.Header.Get("X-Amz-Content-Sha256") != "44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a" {
		t.Fatalf("headers=%v", request.Header)
	}
}

func TestParseAWSCredentialIsStrict(t *testing.T) {
	valid := `{"access_key_id":"AKID","secret_access_key":"secret","session_token":"token"}`
	if credential, err := parseAWSCredential(valid); err != nil || credential.SessionToken != "token" {
		t.Fatalf("credential=%+v err=%v", credential, err)
	}
	for _, raw := range []string{`{}`, `{"access_key_id":"AKID","secret_access_key":"secret","extra":true}`, valid + `{}`} {
		if _, err := parseAWSCredential(raw); err == nil {
			t.Fatalf("accepted credential %s", raw)
		}
	}
}

func TestAWSCanonicalURIEncodesBedrockModelID(t *testing.T) {
	if got := awsCanonicalURI("/model/us.anthropic.claude-v1:0/converse"); got != "/model/us.anthropic.claude-v1%3A0/converse" {
		t.Fatalf("canonical URI=%q", got)
	}
	if got := awsCanonicalURI("/model/escaped%2fmodel/converse"); got != "/model/escaped%2Fmodel/converse" {
		t.Fatalf("escaped canonical URI=%q", got)
	}
}
