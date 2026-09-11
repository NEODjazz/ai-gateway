package assistantstate

import (
	"context"
	"time"
)

const MaxRunSnapshotBytes = 2 << 20
const MaxRunStepSnapshotBytes = 2 << 20

type RunRecord struct {
	ID          string
	ThreadID    string
	OwnerKey    string
	Status      string
	Snapshot    []byte
	Revision    int64
	RetainUntil time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type RunStepRecord struct {
	ID        string
	RunID     string
	ThreadID  string
	OwnerKey  string
	Status    string
	Snapshot  []byte
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type RunPageOptions struct {
	Limit  int
	After  string
	Before string
	Order  string
}

type RunStore interface {
	CreateRun(context.Context, RunRecord, int) (RunRecord, error)
	ListRuns(context.Context, string, string, RunPageOptions) ([]RunRecord, string, error)
	GetRun(context.Context, string, string, string) (RunRecord, error)
	TransitionRun(context.Context, RunRecord, string, int64) (RunRecord, error)
	CreateRunStep(context.Context, RunStepRecord, int) (RunStepRecord, error)
	ListRunSteps(context.Context, string, string, string, RunPageOptions) ([]RunStepRecord, string, error)
	GetRunStep(context.Context, string, string, string, string) (RunStepRecord, error)
	UpdateRunStep(context.Context, RunStepRecord, string, int64) (RunStepRecord, error)
}

func ActiveRunStatus(status string) bool {
	switch status {
	case "queued", "in_progress", "requires_action", "cancelling":
		return true
	default:
		return false
	}
}

func ValidRunStatus(status string) bool {
	return ActiveRunStatus(status) || status == "completed" || status == "failed" || status == "cancelled" || status == "expired" || status == "incomplete"
}

func ValidRunStepStatus(status string) bool {
	return status == "in_progress" || status == "completed" || status == "failed" || status == "cancelled" || status == "expired"
}

func ValidRunTransition(from, to string) bool {
	switch from {
	case "queued":
		return to == "in_progress" || to == "cancelling" || to == "cancelled" || to == "failed" || to == "expired"
	case "in_progress":
		return to == "requires_action" || to == "completed" || to == "failed" || to == "cancelling" || to == "cancelled" || to == "expired" || to == "incomplete"
	case "requires_action":
		return to == "queued" || to == "in_progress" || to == "cancelling" || to == "cancelled" || to == "failed" || to == "expired"
	case "cancelling":
		return to == "cancelled" || to == "failed"
	default:
		return false
	}
}

func ValidRunStepTransition(from, to string) bool {
	return from == "in_progress" && to != "in_progress" && ValidRunStepStatus(to)
}
