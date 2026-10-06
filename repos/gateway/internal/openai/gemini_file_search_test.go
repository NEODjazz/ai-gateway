package openai

import (
	"reflect"
	"testing"
)

func TestGeminiFileSearchConfigValidationAndIdentifiers(t *testing.T) {
	topK := 12
	config := &GeminiFileSearchConfig{StoreNames: []string{"fileSearchStores/support", "fileSearchStores/manuals.v2"}, MetadataFilter: `category="public"`, TopK: &topK}
	if !ValidGeminiFileSearchConfig(config) {
		t.Fatal("valid config rejected")
	}
	want := []string{"file_search", "gemini_file_search:fileSearchStores/support", "gemini_file_search:fileSearchStores/manuals.v2"}
	if got := GeminiFileSearchToolIdentifiers(config); !reflect.DeepEqual(got, want) {
		t.Fatalf("identifiers=%v want=%v", got, want)
	}
	if !ValidGeminiFileSearchMediaID("fileSearchStores/support/media/document-1", "fileSearchStores/support") {
		t.Fatal("valid media ID rejected")
	}
}

func TestGeminiFileSearchConfigRejectsInvalidValues(t *testing.T) {
	zero, tooMany := 0, 101
	tests := []*GeminiFileSearchConfig{
		nil,
		{},
		{StoreNames: []string{"stores/support"}},
		{StoreNames: []string{"fileSearchStores/support", "fileSearchStores/support"}},
		{StoreNames: []string{"fileSearchStores/support"}, MetadataFilter: "x\ny"},
		{StoreNames: []string{"fileSearchStores/support"}, TopK: &zero},
		{StoreNames: []string{"fileSearchStores/support"}, TopK: &tooMany},
	}
	for index, config := range tests {
		if ValidGeminiFileSearchConfig(config) {
			t.Fatalf("invalid config %d accepted: %+v", index, config)
		}
	}
	if ValidGeminiFileSearchMediaID("fileSearchStores/other/media/document-1", "fileSearchStores/support") {
		t.Fatal("cross-store media ID accepted")
	}
}
