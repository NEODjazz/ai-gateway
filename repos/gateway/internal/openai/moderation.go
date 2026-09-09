package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const MaxModerationInputs = 1000

type ModerationRequest struct {
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model,omitempty"`
	Input    any               `json:"input"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type ModerationResponse struct {
	ID      string             `json:"id"`
	Model   string             `json:"model"`
	Results []ModerationResult `json:"results"`
}

type ModerationResult struct {
	Flagged                   bool                `json:"flagged"`
	Categories                map[string]*bool    `json:"categories"`
	CategoryScores            map[string]float64  `json:"category_scores"`
	CategoryAppliedInputTypes map[string][]string `json:"category_applied_input_types"`
}

type ModerationInputInfo struct {
	ResultCount int
	Text        string
}

func InspectModerationInput(value any) (ModerationInputInfo, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return ModerationInputInfo{}, invalidModerationInput()
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ModerationInputInfo{}, invalidModerationInput()
	}
	if raw[0] == '"' {
		var text string
		if json.Unmarshal(raw, &text) != nil || strings.TrimSpace(text) == "" {
			return ModerationInputInfo{}, invalidModerationInput()
		}
		return ModerationInputInfo{ResultCount: 1, Text: text}, nil
	}
	if raw[0] != '[' {
		return ModerationInputInfo{}, invalidModerationInput()
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 || len(items) > MaxModerationInputs {
		return ModerationInputInfo{}, invalidModerationInput()
	}
	first := bytes.TrimSpace(items[0])
	if len(first) == 0 {
		return ModerationInputInfo{}, invalidModerationInput()
	}
	if first[0] == '"' {
		texts := make([]string, len(items))
		for i, item := range items {
			if json.Unmarshal(item, &texts[i]) != nil || strings.TrimSpace(texts[i]) == "" {
				return ModerationInputInfo{}, invalidModerationInput()
			}
		}
		return ModerationInputInfo{ResultCount: len(texts), Text: strings.Join(texts, "\n")}, nil
	}
	texts := make([]string, 0, len(items))
	images := 0
	for _, item := range items {
		var part map[string]json.RawMessage
		if json.Unmarshal(item, &part) != nil || len(part) != 2 {
			return ModerationInputInfo{}, invalidModerationInput()
		}
		var kind string
		if json.Unmarshal(part["type"], &kind) != nil {
			return ModerationInputInfo{}, invalidModerationInput()
		}
		switch kind {
		case "text":
			var text string
			if _, ok := part["text"]; !ok || json.Unmarshal(part["text"], &text) != nil || strings.TrimSpace(text) == "" {
				return ModerationInputInfo{}, invalidModerationInput()
			}
			texts = append(texts, text)
		case "image_url":
			var image map[string]json.RawMessage
			if _, ok := part["image_url"]; !ok || json.Unmarshal(part["image_url"], &image) != nil || len(image) != 1 {
				return ModerationInputInfo{}, invalidModerationInput()
			}
			var value string
			if json.Unmarshal(image["url"], &value) != nil || !validModerationImageURL(value) {
				return ModerationInputInfo{}, invalidModerationInput()
			}
			images++
		default:
			return ModerationInputInfo{}, invalidModerationInput()
		}
	}
	if images > MaxImageAttachments {
		return ModerationInputInfo{}, invalidModerationInput()
	}
	return ModerationInputInfo{ResultCount: 1, Text: strings.Join(texts, "\n")}, nil
}

func validModerationImageURL(value string) bool {
	if strings.HasPrefix(value, "data:") {
		_, err := ParseDataImageURL(value)
		return err == nil
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil && parsed.Fragment == ""
}

func invalidModerationInput() error {
	return errors.New("input must be a non-empty string, an array of non-empty strings, or an array of text and image_url objects within moderation limits")
}

func ModerationInputText(value any) string {
	info, err := InspectModerationInput(value)
	if err != nil {
		return ""
	}
	return info.Text
}

func ModerationInputTokenCount(value any) int {
	return max(1, EstimateContextTokens(ModerationInputText(value)))
}

func ModerationImageAttachments(value any) ([]ImageAttachment, error) {
	return ResponseImageAttachments(value)
}
