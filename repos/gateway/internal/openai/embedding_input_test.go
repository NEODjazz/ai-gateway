package openai

import (
	"strings"
	"testing"
)

func TestInspectEmbeddingInputAcceptsDocumentedShapes(t *testing.T) {
	for _, test := range []struct {
		name       string
		input      any
		kind       EmbeddingInputKind
		count      int
		tokenCount int
	}{
		{"text", "hello", EmbeddingInputSingleText, 1, 0},
		{"texts", []any{"one", "two"}, EmbeddingInputTextList, 2, 0},
		{"tokens", []any{1.0, 2.0, 3.0}, EmbeddingInputTokenIDs, 1, 3},
		{"token arrays", []any{[]any{1.0, 2.0}, []any{3.0}}, EmbeddingInputTokenIDLists, 2, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := InspectEmbeddingInput(test.input)
			if err != nil || result.Kind != test.kind || result.Count != test.count || result.TokenCount != test.tokenCount {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestInspectEmbeddingInputRejectsInvalidAndExcessiveInputs(t *testing.T) {
	tooManyInputs := make([]string, MaxEmbeddingInputs+1)
	for index := range tooManyInputs {
		tooManyInputs[index] = "x"
	}
	tooManyTokens := make([]int, MaxEmbeddingTokensPerInput+1)
	for _, input := range []any{
		nil, "", []any{}, []any{"ok", 1}, []any{-1}, []any{1.5}, []any{2147483648},
		[]any{[]any{}}, []any{[]any{1}, "mixed"}, tooManyInputs, tooManyTokens,
	} {
		if _, err := InspectEmbeddingInput(input); err == nil {
			t.Fatalf("invalid input accepted: %#v", input)
		}
	}

	totalOverflow := make([]any, MaxEmbeddingTokensTotal/MaxEmbeddingTokensPerInput+1)
	for index := range totalOverflow {
		totalOverflow[index] = make([]int, MaxEmbeddingTokensPerInput)
	}
	if _, err := InspectEmbeddingInput(totalOverflow); err == nil {
		t.Fatal("aggregate token limit was ignored")
	}
}

func TestEmbeddingInputTokenCountUsesExactTokenIDsAndSeparateTexts(t *testing.T) {
	if got := EmbeddingInputTokenCount([]any{11.0, 12.0, 13.0}); got != 3 {
		t.Fatalf("token IDs counted as serialized JSON: %d", got)
	}
	want := EstimateContextTokens("first") + EstimateContextTokens("second")
	if got := EmbeddingInputTokenCount([]string{"first", "second"}); got != want {
		t.Fatalf("text inputs counted across an artificial boundary: got=%d want=%d", got, want)
	}
	if _, ok := EmbeddingInputStrings([]any{1.0}); ok {
		t.Fatal("token IDs were exposed as text")
	}
	if text := EmbeddingInputText([]any{1.0}); text != "" {
		t.Fatalf("token IDs were exposed to text policy: %q", text)
	}
	if !strings.Contains(invalidEmbeddingInput().Error(), "token-ID arrays") {
		t.Fatal("validation error does not describe supported shapes")
	}
}
