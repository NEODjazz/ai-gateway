package openai

import "regexp"

var geminiComputerUseFunctionPattern = regexp.MustCompile(`^[A-Za-z0-9_:.\-]{1,128}$`)

type GeminiComputerUseConfig struct {
	Environment                    string   `json:"environment"`
	ExcludedPredefinedFunctions    []string `json:"excludedPredefinedFunctions,omitempty"`
	EnablePromptInjectionDetection bool     `json:"enablePromptInjectionDetection,omitempty"`
	DisabledSafetyPolicies         []string `json:"disabledSafetyPolicies,omitempty"`
}

func ValidGeminiComputerUseConfig(value *GeminiComputerUseConfig) bool {
	if value == nil || value.Environment != "ENVIRONMENT_BROWSER" && value.Environment != "ENVIRONMENT_MOBILE" && value.Environment != "ENVIRONMENT_DESKTOP" || len(value.ExcludedPredefinedFunctions) > 64 || len(value.DisabledSafetyPolicies) > 7 {
		return false
	}
	seenFunctions := map[string]bool{}
	for _, name := range value.ExcludedPredefinedFunctions {
		if !geminiComputerUseFunctionPattern.MatchString(name) || seenFunctions[name] {
			return false
		}
		seenFunctions[name] = true
	}
	validPolicies := map[string]bool{"FINANCIAL_TRANSACTIONS": true, "SENSITIVE_DATA_MODIFICATION": true, "COMMUNICATION_TOOL": true, "ACCOUNT_CREATION": true, "DATA_MODIFICATION": true, "USER_CONSENT_MANAGEMENT": true, "LEGAL_TERMS_AND_AGREEMENTS": true}
	seenPolicies := map[string]bool{}
	for _, policy := range value.DisabledSafetyPolicies {
		if !validPolicies[policy] || seenPolicies[policy] {
			return false
		}
		seenPolicies[policy] = true
	}
	return true
}

func GeminiComputerUseToolIdentifiers(value *GeminiComputerUseConfig) []string {
	if value == nil {
		return nil
	}
	result := []string{"computer_use", "gemini_computer_use:" + value.Environment}
	for _, policy := range value.DisabledSafetyPolicies {
		result = append(result, "gemini_computer_use:disable:"+policy)
	}
	return result
}
