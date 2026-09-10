package asyncstate

import (
	"context"
	"errors"
	"time"
)

const MaxPayloadBytes = 64 << 10

var ErrNotFound = errors.New("async job not found")
var ErrConflict = errors.New("async job conflicts with existing resource")
var ErrLeaseLost = errors.New("async job lease was lost")
var ErrUnavailable = errors.New("async job storage is unavailable")
var ErrInvalid = errors.New("invalid async job")

type Job struct {
	Kind            string
	ResourceID      string
	OwnerKey        string
	EndpointID      string
	ExecutionID     string
	Payload         []byte
	Attempts        int
	LeaseGeneration int64
	AvailableAt     time.Time
	LeaseUntil      time.Time
	CreatedAt       time.Time
}

type Store interface {
	EnqueueAsyncJob(context.Context, Job) (bool, error)
	ClaimAsyncJobs(context.Context, string, int, time.Duration) ([]Job, error)
	RetryAsyncJob(context.Context, string, string, int64, time.Duration) error
	CompleteAsyncJob(context.Context, string, string, int64) error
}

func Valid(job Job) bool {
	return validToken(job.Kind, 64) && validToken(job.ResourceID, 256) && job.OwnerKey != "" && len(job.OwnerKey) <= 256 &&
		validToken(job.EndpointID, 128) && validToken(job.ExecutionID, 128) && len(job.Payload) > 0 && len(job.Payload) <= MaxPayloadBytes
}

func validToken(value string, limit int) bool {
	if value == "" || len(value) > limit {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' || character == '.' || character == ':' {
			continue
		}
		return false
	}
	return true
}
