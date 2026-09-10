package provider

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type awsCredential struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

func parseAWSCredential(raw string) (awsCredential, error) {
	var credential awsCredential
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&credential); err != nil {
		return credential, errors.New("invalid AWS credential")
	}
	if decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(credential.AccessKeyID) == "" || len(credential.AccessKeyID) > 128 || credential.SecretAccessKey == "" || len(credential.SecretAccessKey) > 256 || len(credential.SessionToken) > 4096 {
		return credential, errors.New("invalid AWS credential")
	}
	return credential, nil
}

func signAWSRequest(request *http.Request, payload []byte, credential awsCredential, region, service string, now time.Time) error {
	if request == nil || !validBedrockRegion(region) || service == "" || request.URL.RawQuery != "" || request.URL.Fragment != "" {
		return errors.New("invalid AWS signing configuration")
	}
	payloadHash := sha256Hex(payload)
	amzDate := now.UTC().Format("20060102T150405Z")
	shortDate := now.UTC().Format("20060102")
	request.Header.Set("X-Amz-Date", amzDate)
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if credential.SessionToken != "" {
		request.Header.Set("X-Amz-Security-Token", credential.SessionToken)
	}
	headerNames := []string{"content-type", "host", "x-amz-content-sha256", "x-amz-date"}
	if credential.SessionToken != "" {
		headerNames = append(headerNames, "x-amz-security-token")
	}
	sort.Strings(headerNames)
	var canonicalHeaders strings.Builder
	for _, name := range headerNames {
		value := request.Header.Get(name)
		if name == "host" {
			value = request.URL.Host
		}
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.Join(strings.Fields(value), " "))
		canonicalHeaders.WriteByte('\n')
	}
	signedHeaders := strings.Join(headerNames, ";")
	canonicalRequest := request.Method + "\n" + awsCanonicalURI(request.URL.EscapedPath()) + "\n\n" + canonicalHeaders.String() + "\n" + signedHeaders + "\n" + payloadHash
	scope := shortDate + "/" + region + "/" + service + "/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))
	dateKey := hmacSHA256([]byte("AWS4"+credential.SecretAccessKey), shortDate)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, service)
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	request.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", credential.AccessKeyID, scope, signedHeaders, signature))
	return nil
}

func awsCanonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	const hexDigits = "0123456789ABCDEF"
	var canonical strings.Builder
	canonical.Grow(len(path))
	for index := 0; index < len(path); index++ {
		value := path[index]
		if value == '/' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || value == '-' || value == '.' || value == '_' || value == '~' {
			canonical.WriteByte(value)
			continue
		}
		if value == '%' && index+2 < len(path) && isHex(path[index+1]) && isHex(path[index+2]) {
			first, second := path[index+1], path[index+2]
			if first >= 'a' && first <= 'f' {
				first -= 'a' - 'A'
			}
			if second >= 'a' && second <= 'f' {
				second -= 'a' - 'A'
			}
			canonical.WriteByte('%')
			canonical.WriteByte(first)
			canonical.WriteByte(second)
			index += 2
			continue
		}
		canonical.WriteByte('%')
		canonical.WriteByte(hexDigits[value>>4])
		canonical.WriteByte(hexDigits[value&15])
	}
	return canonical.String()
}

func isHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, value string) []byte {
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte(value))
	return digest.Sum(nil)
}
