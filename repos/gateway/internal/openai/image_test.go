package openai

import (
	"strings"
	"testing"
)

func TestImageGenerationRequestValidation(t *testing.T) {
	n0, n11, compression := 0, 11, 101
	for _, request := range []ImageGenerationRequest{
		{},
		{Model: "image", Prompt: strings.Repeat("x", 32001)},
		{Model: "image", Prompt: "draw", N: &n0},
		{Model: "image", Prompt: "draw", N: &n11},
		{Model: "image", Prompt: "draw", OutputCompression: &compression},
		{Model: "image", Prompt: "draw", OutputFormat: "gif"},
		{Model: "image", Prompt: "draw", User: strings.Repeat("u", 257)},
	} {
		if request.Validate() == "" {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	n := 10
	valid := ImageGenerationRequest{Model: "image", Prompt: "draw", N: &n}
	if message := valid.Validate(); message != "" {
		t.Fatal(message)
	}
	if got := ImageGenerationOutputReserve(valid); got != 10*DefaultOutputTokenReserve {
		t.Fatalf("output reserve=%d", got)
	}
	if got := ImageGenerationReserveTokens(valid); got != EstimateContextTokens(valid.Prompt)+10*DefaultOutputTokenReserve {
		t.Fatalf("total reserve=%d", got)
	}
}
