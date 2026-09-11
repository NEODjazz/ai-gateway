package openai

import "encoding/json"

type FineTuningCreateRequest struct {
	Model          string            `json:"model"`
	TrainingFile   string            `json:"training_file"`
	ValidationFile string            `json:"validation_file,omitempty"`
	Suffix         string            `json:"suffix,omitempty"`
	Seed           *int64            `json:"seed,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	Method         json.RawMessage   `json:"method,omitempty"`
}

type FineTuningJob struct {
	ID              string            `json:"id"`
	Object          string            `json:"object"`
	CreatedAt       int64             `json:"created_at"`
	FinishedAt      *int64            `json:"finished_at"`
	FineTunedModel  *string           `json:"fine_tuned_model"`
	Model           string            `json:"model"`
	OrganizationID  string            `json:"organization_id"`
	ResultFiles     []string          `json:"result_files"`
	Status          string            `json:"status"`
	TrainedTokens   *int64            `json:"trained_tokens"`
	TrainingFile    string            `json:"training_file"`
	ValidationFile  *string           `json:"validation_file"`
	Error           json.RawMessage   `json:"error"`
	Hyperparameters json.RawMessage   `json:"hyperparameters,omitempty"`
	Integrations    []json.RawMessage `json:"integrations,omitempty"`
	Seed            int64             `json:"seed,omitempty"`
	EstimatedFinish *int64            `json:"estimated_finish,omitempty"`
	Method          json.RawMessage   `json:"method,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

type FineTuningJobList struct {
	Object  string          `json:"object"`
	Data    []FineTuningJob `json:"data"`
	HasMore bool            `json:"has_more"`
}

type FineTuningEvent struct {
	ID        string          `json:"id"`
	Object    string          `json:"object"`
	CreatedAt int64           `json:"created_at"`
	Level     string          `json:"level"`
	Message   string          `json:"message"`
	Type      string          `json:"type,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
}

type FineTuningEventList struct {
	Object  string            `json:"object"`
	Data    []FineTuningEvent `json:"data"`
	HasMore bool              `json:"has_more"`
}

type FineTuningCheckpoint struct {
	ID              string                     `json:"id"`
	Object          string                     `json:"object"`
	CreatedAt       int64                      `json:"created_at"`
	FineTunedModel  string                     `json:"fine_tuned_model_checkpoint"`
	FineTuningJobID string                     `json:"fine_tuning_job_id"`
	Metrics         map[string]json.RawMessage `json:"metrics"`
	StepNumber      int                        `json:"step_number"`
}

type FineTuningCheckpointList struct {
	Object  string                 `json:"object"`
	Data    []FineTuningCheckpoint `json:"data"`
	HasMore bool                   `json:"has_more"`
}

type ModelDeletion struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}
