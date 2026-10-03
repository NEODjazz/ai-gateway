package openai

import "testing"

func TestValidGeminiMCPServerIDs(t *testing.T) {
	if !ValidGeminiMCPServerIDs([]string{"weather", "finance.prod"}) {
		t.Fatal("valid registry IDs rejected")
	}
	for _, values := range [][]string{nil, {"bad/id"}, {"weather", "weather"}, make([]string, MaxGeminiMCPServers+1)} {
		if ValidGeminiMCPServerIDs(values) {
			t.Fatalf("invalid registry IDs accepted: %#v", values)
		}
	}
}
