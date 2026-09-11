package openai

import "testing"

func TestBatchCreateRequestValidation(t *testing.T) {
	valid := BatchCreateRequest{InputFileID: "file_input", Endpoint: "/v1/responses", CompletionWindow: "24h"}
	if message := valid.Validate(); message != "" {
		t.Fatalf("valid request: %s", message)
	}
	for _, test := range []BatchCreateRequest{
		{Endpoint: valid.Endpoint, CompletionWindow: valid.CompletionWindow},
		{InputFileID: valid.InputFileID, Endpoint: "/v1/images/generations", CompletionWindow: valid.CompletionWindow},
		{InputFileID: valid.InputFileID, Endpoint: valid.Endpoint, CompletionWindow: "48h"},
	} {
		if message := test.Validate(); message == "" {
			t.Fatalf("invalid request accepted: %+v", test)
		}
	}
}
