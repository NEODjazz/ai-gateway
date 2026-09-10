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
		{Model: "image", Prompt: "draw", Resolution: "8K"},
		{Model: "image", Prompt: "draw", AspectRatio: "16x9"},
		{Model: "image", Prompt: "draw", AspectRatio: "0:1"},
		{Model: "image", Prompt: "draw", OutputFormat: "gif"},
		{Model: "image", Prompt: "draw", User: strings.Repeat("u", 257)},
	} {
		if request.Validate() == "" {
			t.Fatalf("invalid request accepted: %+v", request)
		}
	}
	for _, ratio := range []string{"auto", "1:1", "16:9", "9:21", "99:99"} {
		if message := (ImageGenerationRequest{Model: "image", Prompt: "draw", AspectRatio: ratio}).Validate(); message != "" {
			t.Fatalf("valid aspect ratio %q rejected: %s", ratio, message)
		}
	}
	if message := (ImageGenerationRequest{Model: "image", Prompt: "draw", Resolution: "2K", OutputFormat: "svg"}).Validate(); message != "" {
		t.Fatalf("normalized image controls rejected: %s", message)
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

func TestImageEditRequestValidationAndReserve(t *testing.T) {
	attachment, err := ParseDataImageURL("data:image/png;base64,iVBORw0KGgpmaXh0dXJl")
	if err != nil {
		t.Fatal(err)
	}
	n := 2
	request := ImageEditRequest{Model: "image", Prompt: "edit", Images: []ImageAttachment{attachment}, Mask: &attachment, N: &n}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	if got := ImageEditReserveTokens(request); got != ImageEditInputTokens(request)+2*DefaultOutputTokenReserve {
		t.Fatalf("reserve=%d", got)
	}
	if ImageEditInputTokens(request) <= EstimateContextTokens(request.Prompt) {
		t.Fatal("image bytes were not included in input estimate")
	}
	request.Images[0].MediaType = "image/jpeg"
	if message := request.Validate(); message == "" {
		t.Fatal("mismatched image media type was accepted")
	}
}

func TestImageVariationRequestValidationAndReserve(t *testing.T) {
	attachment, err := ParseDataImageURL("data:image/png;base64,iVBORw0KGgpmaXh0dXJl")
	if err != nil {
		t.Fatal(err)
	}
	n := 2
	request := ImageVariationRequest{Model: "image", Image: attachment, N: &n, Size: "512x512"}
	if message := request.Validate(); message != "" {
		t.Fatal(message)
	}
	if got := ImageVariationReserveTokens(request); got != ImageVariationInputTokens(request)+2*DefaultOutputTokenReserve || ImageVariationInputTokens(request) == 0 {
		t.Fatalf("input=%d reserve=%d", ImageVariationInputTokens(request), got)
	}
	request.Size = "1792x1024"
	if message := request.Validate(); message == "" {
		t.Fatal("unsupported variation size was accepted")
	}
}
