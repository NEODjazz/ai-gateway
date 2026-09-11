package openai

import "encoding/json"

type VideoInputReference struct {
	FileID   string `json:"file_id,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type VideoCreateRequest struct {
	Model          string               `json:"model,omitempty"`
	Prompt         string               `json:"prompt"`
	InputReference *VideoInputReference `json:"input_reference,omitempty"`
	Seconds        string               `json:"seconds,omitempty"`
	Size           string               `json:"size,omitempty"`
}

type VideoRemixRequest struct {
	Prompt string `json:"prompt"`
}

type Video struct {
	ID                 string          `json:"id"`
	Object             string          `json:"object"`
	Model              string          `json:"model"`
	Status             string          `json:"status"`
	Progress           float64         `json:"progress"`
	CreatedAt          int64           `json:"created_at"`
	CompletedAt        *int64          `json:"completed_at"`
	ExpiresAt          *int64          `json:"expires_at"`
	Prompt             *string         `json:"prompt"`
	RemixedFromVideoID *string         `json:"remixed_from_video_id"`
	Seconds            string          `json:"seconds"`
	Size               string          `json:"size"`
	Quality            string          `json:"quality,omitempty"`
	Error              json.RawMessage `json:"error"`
}

type VideoList struct {
	Object  string  `json:"object"`
	Data    []Video `json:"data"`
	FirstID string  `json:"first_id,omitempty"`
	LastID  string  `json:"last_id,omitempty"`
	HasMore bool    `json:"has_more"`
}

type VideoDeletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}
