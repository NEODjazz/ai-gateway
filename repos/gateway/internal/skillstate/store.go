package skillstate

import (
	"context"
	"errors"
	"time"
)

var ErrNotFound = errors.New("skill ownership not found")
var ErrConflict = errors.New("skill ownership already exists")
var ErrUnavailable = errors.New("skill ownership storage is unavailable")
var ErrInvalid = errors.New("invalid skill ownership request")

type Ownership struct {
	SkillID    string
	OwnerKey   string
	EndpointID string
	CreatedAt  time.Time
}

type Execution struct {
	ContainerID string
	OwnerKey    string
	EndpointID  string
	ExpiresAt   time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type ExecutionStore interface {
	SaveSkillExecution(context.Context, Execution) error
	ResolveSkillExecution(context.Context, string, string) (Execution, error)
}

type Store interface {
	ClaimSkill(context.Context, Ownership) (Ownership, error)
	ResolveSkill(context.Context, string, string) (Ownership, error)
	OwnedSkills(context.Context, string, string, []string) (map[string]bool, error)
	DeleteSkill(context.Context, string, string) error
}
