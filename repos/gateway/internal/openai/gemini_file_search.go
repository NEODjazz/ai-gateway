package openai

import (
	"regexp"
	"strings"
)

const MaxGeminiFileSearchStores = 20

var geminiFileSearchStorePattern = regexp.MustCompile(`^fileSearchStores/[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type GeminiFileSearchConfig struct {
	StoreNames     []string `json:"fileSearchStoreNames"`
	MetadataFilter string   `json:"metadataFilter,omitempty"`
	TopK           *int     `json:"topK,omitempty"`
}

func ValidGeminiFileSearchConfig(value *GeminiFileSearchConfig) bool {
	if value == nil || len(value.StoreNames) == 0 || len(value.StoreNames) > MaxGeminiFileSearchStores || len(value.MetadataFilter) > 2048 || strings.ContainsAny(value.MetadataFilter, "\r\n\x00") {
		return false
	}
	seen := make(map[string]bool, len(value.StoreNames))
	for _, name := range value.StoreNames {
		if !ValidGeminiFileSearchStoreName(name) || seen[name] {
			return false
		}
		seen[name] = true
	}
	return value.TopK == nil || *value.TopK >= 1 && *value.TopK <= 100
}

func ValidGeminiFileSearchStoreName(value string) bool {
	return geminiFileSearchStorePattern.MatchString(value)
}

func ValidGeminiFileSearchMediaID(value, store string) bool {
	prefix := store + "/media/"
	return ValidGeminiFileSearchStoreName(store) && len(value) <= 512 && strings.HasPrefix(value, prefix) && geminiFileSearchStorePattern.MatchString("fileSearchStores/"+strings.TrimPrefix(value, prefix))
}

func GeminiFileSearchToolIdentifiers(value *GeminiFileSearchConfig) []string {
	if value == nil {
		return nil
	}
	result := make([]string, 0, len(value.StoreNames)+1)
	result = append(result, "file_search")
	for _, name := range value.StoreNames {
		result = append(result, "gemini_file_search:"+name)
	}
	return result
}
