package openai

import (
	"reflect"
	"testing"
)

func TestGeminiComputerUseValidationAndIdentifiers(t *testing.T) {
	config := &GeminiComputerUseConfig{Environment: "ENVIRONMENT_DESKTOP", ExcludedPredefinedFunctions: []string{"click", "type_text"}, EnablePromptInjectionDetection: true, DisabledSafetyPolicies: []string{"DATA_MODIFICATION"}}
	if !ValidGeminiComputerUseConfig(config) {
		t.Fatal("valid config rejected")
	}
	want := []string{"computer_use", "gemini_computer_use:ENVIRONMENT_DESKTOP", "gemini_computer_use:disable:DATA_MODIFICATION"}
	if got := GeminiComputerUseToolIdentifiers(config); !reflect.DeepEqual(got, want) {
		t.Fatalf("identifiers=%v want=%v", got, want)
	}
	for _, invalid := range []*GeminiComputerUseConfig{nil, {}, {Environment: "browser"}, {Environment: "ENVIRONMENT_BROWSER", ExcludedPredefinedFunctions: []string{"click", "click"}}, {Environment: "ENVIRONMENT_BROWSER", DisabledSafetyPolicies: []string{"UNKNOWN"}}} {
		if ValidGeminiComputerUseConfig(invalid) {
			t.Fatalf("invalid config accepted: %+v", invalid)
		}
	}
}
