package openai

import "time"

func ValidGeminiSearchTimeRange(value *GeminiSearchTimeRange) bool {
	if value == nil {
		return true
	}
	start, startErr := time.Parse(time.RFC3339Nano, value.StartTime)
	end, endErr := time.Parse(time.RFC3339Nano, value.EndTime)
	return startErr == nil && endErr == nil && !start.After(end)
}
