package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-anonymizer/internal/modules"
	"ai-gateway-anonymizer/internal/openai"
)

func TestAnonymizeReturnsJSONContentType(t *testing.T) {
	handler := newHandler(modules.NewAnonymizerModule(true, modules.RuleEmail))
	request := httptest.NewRequest(http.MethodPost, "/anonymize", strings.NewReader(`{"request_id":"req-1","messages":[{"role":"user","content":"user@example.com"}]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unexpected status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("unexpected Content-Type %q", contentType)
	}
	var body anonymizeResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if got := openai.ContentText(body.Messages[0].Content); got != "{{EMAIL_1}}" || body.Replacements["{{EMAIL_1}}"] != "user@example.com" {
		t.Fatalf("unexpected anonymized response: %+v", body)
	}
}

func TestAnonymizerHealthReturns204(t *testing.T) {
	response := httptest.NewRecorder()
	newHandler(modules.NewAnonymizerModule(true)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}
