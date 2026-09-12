package openai

import "testing"

func TestBatchCreateRequestValidation(t *testing.T) {
	valid := BatchCreateRequest{InputFileID: "file_input", CompletionWindow: "24h"}
	for _, endpoint := range []string{"/v1/responses", "/v1/responses/compact", "/v1/rerank", "/v1/search", "/v1/images/generations", "/v1/audio/speech", "/v1/ocr"} {
		request := valid
		request.Endpoint = endpoint
		if message := request.Validate(); message != "" {
			t.Fatalf("valid endpoint %s: %s", endpoint, message)
		}
	}
	valid.Endpoint = "/v1/responses"
	for _, test := range []BatchCreateRequest{
		{Endpoint: valid.Endpoint, CompletionWindow: valid.CompletionWindow},
		{InputFileID: valid.InputFileID, Endpoint: "/v1/unknown", CompletionWindow: valid.CompletionWindow},
		{InputFileID: valid.InputFileID, Endpoint: valid.Endpoint, CompletionWindow: "48h"},
	} {
		if message := test.Validate(); message == "" {
			t.Fatalf("invalid request accepted: %+v", test)
		}
	}
}
