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

func TestAnonymizeRerankProjection(t *testing.T) {
	handler := newHandler(modules.NewAnonymizerModule(true, modules.RuleEmail))
	request := httptest.NewRequest(http.MethodPost, "/anonymize", strings.NewReader(`{"request_id":"req-rerank","query":"find user@example.com","documents":["contact user@example.com",{"text":"owner@example.com","id":"doc-1"}]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body anonymizeResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body.Query, "{{EMAIL_1}}") || len(body.Documents) != 2 || len(body.Replacements) != 2 {
		t.Fatalf("unexpected response: %+v", body)
	}
}

func TestAnonymizeSelectsRulesPerRequest(t *testing.T) {
	handler := newHandler(modules.NewAnonymizerModule(true, modules.RuleEmail, modules.RulePhone))
	request := httptest.NewRequest(http.MethodPost, "/anonymize", strings.NewReader(`{"messages":[{"role":"user","content":"user@example.com +7 999 123-45-67"}],"rules":["email"]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var body anonymizeResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	content := openai.ContentText(body.Messages[0].Content)
	if !strings.Contains(content, "{{EMAIL_1}}") || !strings.Contains(content, "+7 999 123-45-67") || strings.Contains(content, "{{PHONE") {
		t.Fatalf("unexpected selected-rule response: %+v", body)
	}
}

func TestAnonymizeRejectsUnknownRequestedRule(t *testing.T) {
	handler := newHandler(modules.NewAnonymizerModule(true, modules.RuleEmail))
	request := httptest.NewRequest(http.MethodPost, "/anonymize", strings.NewReader(`{"messages":[{"role":"user","content":"user@example.com"}],"rules":["missing"]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAnonymizerHealthReturns204(t *testing.T) {
	response := httptest.NewRecorder()
	newHandler(modules.NewAnonymizerModule(true)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", response.Code)
	}
}
