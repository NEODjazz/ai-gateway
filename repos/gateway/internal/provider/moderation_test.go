package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

func validModerationResponseJSON() string {
	return `{"id":"modr-1","model":"moderation-upstream","results":[{"flagged":false,"categories":{"violence":false},"category_scores":{"violence":0.1},"category_applied_input_types":{"violence":["text"]}}]}`
}

func TestOpenAICompatibleModerationsContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/moderations" || r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["model"] != "moderation-route" || request["input"] != "inspect me" {
			t.Fatalf("unexpected payload: %+v", request)
		}
		_, _ = w.Write([]byte(validModerationResponseJSON()))
	}))
	defer server.Close()
	client := NewOpenAICompatible(server.URL+"/v1", "secret", false)
	response, err := client.Moderations(context.Background(), openai.ModerationRequest{Model: "moderation-route", Input: "inspect me"})
	if err != nil || response.ID != "modr-1" || response.Model != "moderation-upstream" {
		t.Fatalf("unexpected response: %+v err=%v", response, err)
	}
}

func TestOpenAICompatibleModerationsRejectsMetadataBeforeUpstream(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	client := NewOpenAICompatible(server.URL, "secret", false)
	_, err := client.Moderations(t.Context(), openai.ModerationRequest{
		Model: "moderation-route", Input: "inspect me", Metadata: map[string]string{"trace": "moderation"},
	})
	var failure *Error
	if !errors.As(err, &failure) || failure.Provider != "openai-compatible" || failure.Param != "metadata" || failure.UpstreamCode != "unsupported_parameter" || calls != 0 {
		t.Fatalf("error=%v failure=%+v upstream calls=%d", err, failure, calls)
	}
}

func TestDecodeAndValidateModerationResponse(t *testing.T) {
	if _, err := decodeModerationResponse(strings.NewReader(validModerationResponseJSON() + `{}`)); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
	response, err := decodeModerationResponse(strings.NewReader(validModerationResponseJSON()))
	if err != nil || validateModerationResponse(response, 1) != nil {
		t.Fatalf("valid response rejected: %v", err)
	}
	response.Results[0].CategoryScores["violence"] = 2
	if err := validateModerationResponse(response, 1); err == nil {
		t.Fatal("expected out-of-range score rejection")
	}
	response.Results[0].CategoryScores["violence"] = .1
	response.Results[0].Flagged = true
	if err := validateModerationResponse(response, 1); err == nil {
		t.Fatal("expected inconsistent flag rejection")
	}
}
