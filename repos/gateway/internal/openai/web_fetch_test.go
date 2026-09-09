package openai

import "testing"

func TestChatWebFetchOptionsValidation(t *testing.T) {
	maximum := 3
	valid := ChatGenerationOptions{WebFetchOptions: &ChatWebFetchOptions{AllowedDomains: []string{"docs.example.com"}, MaxUses: &maximum, MaxContentTokens: 20000}}
	if message := valid.Validate(); message != "" {
		t.Fatal(message)
	}
	zero, tooMany := 0, WebFetchMaxUses+1
	tests := []*ChatWebFetchOptions{
		{},
		{AllowedDomains: []string{"https://example.com"}, MaxContentTokens: 1},
		{AllowedDomains: []string{"example.com", "example.com"}, MaxContentTokens: 1},
		{AllowedDomains: []string{"example.com"}, MaxUses: &zero, MaxContentTokens: 1},
		{AllowedDomains: []string{"example.com"}, MaxUses: &tooMany, MaxContentTokens: 1},
		{AllowedDomains: []string{"example.com"}, MaxContentTokens: 0},
		{AllowedDomains: []string{"example.com"}, MaxContentTokens: WebFetchMaxContentTokens + 1},
	}
	for index, options := range tests {
		if message := (ChatGenerationOptions{WebFetchOptions: options}).Validate(); message == "" {
			t.Fatalf("case %d accepted: %+v", index, options)
		}
	}
}
