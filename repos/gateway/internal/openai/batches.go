package openai

import "encoding/json"

const (
	BatchCompletionWindow = "24h"
	MaxBatchRequests      = 50000
)

type BatchCreateRequest struct {
	InputFileID        string                 `json:"input_file_id"`
	Endpoint           string                 `json:"endpoint"`
	CompletionWindow   string                 `json:"completion_window"`
	Metadata           map[string]string      `json:"metadata,omitempty"`
	OutputExpiresAfter *BatchOutputExpiration `json:"output_expires_after,omitempty"`
}

type BatchOutputExpiration struct {
	Anchor  string `json:"anchor"`
	Seconds int64  `json:"seconds"`
}

func (r BatchCreateRequest) Validate() string {
	if r.InputFileID == "" {
		return "input_file_id is required"
	}
	switch r.Endpoint {
	case "/v1/responses", "/v1/chat/completions", "/v1/embeddings", "/v1/completions", "/v1/moderations", "/v1/rerank", "/v1/search":
	default:
		return "endpoint is not supported"
	}
	if r.CompletionWindow != BatchCompletionWindow {
		return "completion_window must be 24h"
	}
	if r.OutputExpiresAfter != nil && (r.OutputExpiresAfter.Anchor != "created_at" || r.OutputExpiresAfter.Seconds < 3600 || r.OutputExpiresAfter.Seconds > 2592000) {
		return "output_expires_after requires anchor=created_at and seconds between 3600 and 2592000"
	}
	if len(r.Metadata) > 16 {
		return "metadata cannot contain more than 16 entries"
	}
	for key, value := range r.Metadata {
		if key == "" || len(key) > 64 || len(value) > 512 {
			return "metadata keys and values are invalid"
		}
	}
	return ""
}

type BatchRequestLine struct {
	CustomID string          `json:"custom_id"`
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Body     json.RawMessage `json:"body"`
}

type BatchRequestCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

type BatchErrors struct {
	Object string       `json:"object"`
	Data   []BatchError `json:"data"`
}

type BatchError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Param   string `json:"param,omitempty"`
	Line    *int   `json:"line,omitempty"`
}

type Batch struct {
	ID               string             `json:"id"`
	Object           string             `json:"object"`
	Endpoint         string             `json:"endpoint"`
	Errors           *BatchErrors       `json:"errors"`
	InputFileID      string             `json:"input_file_id"`
	CompletionWindow string             `json:"completion_window"`
	Status           string             `json:"status"`
	OutputFileID     string             `json:"output_file_id,omitempty"`
	ErrorFileID      string             `json:"error_file_id,omitempty"`
	CreatedAt        int64              `json:"created_at"`
	InProgressAt     int64              `json:"in_progress_at,omitempty"`
	ExpiresAt        int64              `json:"expires_at,omitempty"`
	FinalizingAt     int64              `json:"finalizing_at,omitempty"`
	CompletedAt      int64              `json:"completed_at,omitempty"`
	FailedAt         int64              `json:"failed_at,omitempty"`
	ExpiredAt        int64              `json:"expired_at,omitempty"`
	CancellingAt     int64              `json:"cancelling_at,omitempty"`
	CancelledAt      int64              `json:"cancelled_at,omitempty"`
	RequestCounts    BatchRequestCounts `json:"request_counts"`
	Metadata         map[string]string  `json:"metadata,omitempty"`
}

type BatchList struct {
	Object  string  `json:"object"`
	Data    []Batch `json:"data"`
	FirstID string  `json:"first_id,omitempty"`
	LastID  string  `json:"last_id,omitempty"`
	HasMore bool    `json:"has_more"`
}

type BatchOutputLine struct {
	ID       string                `json:"id"`
	CustomID string                `json:"custom_id"`
	Response *BatchOutputResponse  `json:"response"`
	Error    *BatchOutputLineError `json:"error"`
}

type BatchOutputResponse struct {
	StatusCode int             `json:"status_code"`
	RequestID  string          `json:"request_id"`
	Body       json.RawMessage `json:"body"`
}

type BatchOutputLineError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
