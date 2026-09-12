package openai

import (
	"strings"
	"testing"
)

func TestSandboxRequestDefaultsValidationAndTokenReserve(t *testing.T) {
	request := SandboxExecuteRequest{Model: "code-interpreter", Code: "print('hello')"}
	request.ApplyDefaults()
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	if request.Language != DefaultSandboxLanguage || request.Template != DefaultSandboxTemplate || request.TimeoutSeconds != DefaultSandboxTimeoutSeconds || request.InputTokens() < 1 {
		t.Fatalf("request=%+v tokens=%d", request, request.InputTokens())
	}
	for _, invalid := range []SandboxExecuteRequest{
		{Model: "m", Code: strings.Repeat("x", MaxSandboxCodeBytes+1)},
		{Model: "m", Code: "x", Language: "python\ninvalid"},
		{Model: "m", Code: "x", TimeoutSeconds: 901},
	} {
		invalid.ApplyDefaults()
		if invalid.Validate() == "" {
			t.Fatalf("invalid request accepted: %+v", invalid)
		}
	}
}
