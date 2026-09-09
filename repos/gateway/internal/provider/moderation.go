package provider

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const maxModerationResponseBytes = 8 << 20

func decodeModerationResponse(reader io.Reader) (openai.ModerationResponse, error) {
	var response openai.ModerationResponse
	payload, err := io.ReadAll(io.LimitReader(reader, maxModerationResponseBytes+1))
	if err != nil {
		return response, err
	}
	if len(payload) > maxModerationResponseBytes {
		return response, errors.New("moderation response exceeds limit")
	}
	if err := json.Unmarshal(payload, &response); err != nil {
		return response, err
	}
	return response, nil
}

func validateModerationResponse(response openai.ModerationResponse, resultCount int) error {
	if strings.TrimSpace(response.ID) == "" || len(response.ID) > 512 || strings.TrimSpace(response.Model) == "" || len(response.Model) > 512 {
		return errors.New("provider returned invalid moderation identity")
	}
	if len(response.Results) != resultCount {
		return errors.New("moderation response count does not match input")
	}
	for _, result := range response.Results {
		if len(result.Categories) == 0 || len(result.Categories) > 128 || len(result.CategoryScores) != len(result.Categories) || len(result.CategoryAppliedInputTypes) != len(result.Categories) {
			return errors.New("provider returned invalid moderation categories")
		}
		flagged := false
		for category, value := range result.Categories {
			if strings.TrimSpace(category) == "" || len(category) > 128 {
				return errors.New("provider returned invalid moderation category")
			}
			score, scoreOK := result.CategoryScores[category]
			inputTypes, typesOK := result.CategoryAppliedInputTypes[category]
			if !scoreOK || !typesOK || math.IsNaN(score) || math.IsInf(score, 0) || score < 0 || score > 1 || len(inputTypes) == 0 || len(inputTypes) > 2 {
				return errors.New("provider returned invalid moderation category details")
			}
			seen := map[string]bool{}
			for _, inputType := range inputTypes {
				if (inputType != "text" && inputType != "image") || seen[inputType] {
					return errors.New("provider returned invalid moderation input types")
				}
				seen[inputType] = true
			}
			if value != nil && *value {
				flagged = true
			}
		}
		if result.Flagged != flagged {
			return errors.New("provider returned inconsistent moderation flag")
		}
	}
	return nil
}
