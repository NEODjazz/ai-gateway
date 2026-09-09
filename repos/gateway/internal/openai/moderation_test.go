package openai

import "testing"

func TestInspectModerationInput(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		count   int
		text    string
		invalid bool
	}{
		{name: "single", input: "hello", count: 1, text: "hello"},
		{name: "batch", input: []any{"one", "two"}, count: 2, text: "one\ntwo"},
		{name: "multimodal", input: []any{map[string]any{"type": "text", "text": "inspect"}, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/image.png"}}}, count: 1, text: "inspect"},
		{name: "mixed batch", input: []any{"one", map[string]any{"type": "text", "text": "two"}}, invalid: true},
		{name: "unknown part field", input: []any{map[string]any{"type": "text", "text": "one", "extra": true}}, invalid: true},
		{name: "image userinfo", input: []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://user@example.test/a.png"}}}, invalid: true},
		{name: "empty", input: " ", invalid: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info, err := InspectModerationInput(test.input)
			if test.invalid {
				if err == nil {
					t.Fatal("expected invalid input")
				}
				return
			}
			if err != nil || info.ResultCount != test.count || info.Text != test.text {
				t.Fatalf("unexpected result: info=%+v err=%v", info, err)
			}
		})
	}
}

func TestModerationInputTokenCountIncludesAllText(t *testing.T) {
	input := []any{map[string]any{"type": "text", "text": "first context"}, map[string]any{"type": "text", "text": "second context"}}
	if got, want := ModerationInputTokenCount(input), EstimateContextTokens("first context\nsecond context"); got != want {
		t.Fatalf("token count=%d want=%d", got, want)
	}
}
