package provider

import (
	"context"
	"crypto/sha256"
	"errors"
	"math"
	"sync"
	"time"
)

const memoryDeploymentQuotaCapacity = 10_000

// DeploymentQuotaStore atomically reserves one request and its estimated token
// load in a fixed window. Implementations are shared by every inference path.
type DeploymentQuotaStore interface {
	Allow(context.Context, string, int, int, int, time.Duration) (bool, time.Duration, error)
}

type DeploymentQuotaError struct {
	Deployment string
	RetryAfter time.Duration
}

func (e *DeploymentQuotaError) Error() string {
	return e.Deployment + " deployment rate limit exceeded"
}

type deploymentQuotaWindow struct {
	started  time.Time
	requests int
	tokens   int
}

// MemoryDeploymentQuotaStore is bounded and suitable for a single gateway
// replica. Production replicas share the same contract through Redis.
type MemoryDeploymentQuotaStore struct {
	mu      sync.Mutex
	windows map[[32]byte]deploymentQuotaWindow
	now     func() time.Time
}

func NewMemoryDeploymentQuotaStore() *MemoryDeploymentQuotaStore {
	return &MemoryDeploymentQuotaStore{windows: make(map[[32]byte]deploymentQuotaWindow), now: time.Now}
}

func (s *MemoryDeploymentQuotaStore) Allow(ctx context.Context, key string, requestLimit, tokenLimit, tokens int, window time.Duration) (bool, time.Duration, error) {
	if s == nil || key == "" || requestLimit <= 0 && tokenLimit <= 0 {
		return true, 0, nil
	}
	if err := ctx.Err(); err != nil {
		return false, 0, err
	}
	if window <= 0 {
		window = time.Minute
	}
	tokens = max(tokens, 0)
	identity := sha256.Sum256([]byte(key))
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	current, found := s.windows[identity]
	if found && now.Sub(current.started) >= window {
		delete(s.windows, identity)
		found = false
	}
	if !found {
		if len(s.windows) >= memoryDeploymentQuotaCapacity {
			for candidate, value := range s.windows {
				if now.Sub(value.started) >= window {
					delete(s.windows, candidate)
				}
			}
		}
		if len(s.windows) >= memoryDeploymentQuotaCapacity {
			return false, 0, errors.New("memory deployment quota capacity exceeded")
		}
		current.started = now
	}
	if requestLimit > 0 && current.requests >= requestLimit ||
		tokenLimit > 0 && (current.tokens > tokenLimit || tokens > tokenLimit-current.tokens) {
		return false, max(window-now.Sub(current.started), time.Millisecond), nil
	}
	current.requests = saturatedQuotaCount(current.requests, 1)
	current.tokens = saturatedQuotaCount(current.tokens, tokens)
	s.windows[identity] = current
	return true, 0, nil
}

func saturatedQuotaCount(current, increment int) int {
	if increment > math.MaxInt-current {
		return math.MaxInt
	}
	return current + increment
}

func (r Router) acquireEndpoint(ctx context.Context, endpoint Endpoint, tokens int) (func(), error) {
	release, err := endpoint.Admission.acquire(ctx, endpoint.Name)
	if err != nil {
		return nil, err
	}
	if endpoint.RateLimitRPM <= 0 && endpoint.RateLimitTPM <= 0 {
		return release, nil
	}
	if r.deploymentQuotas == nil {
		release()
		return nil, errors.New("deployment quota store unavailable")
	}
	allowed, retryAfter, err := r.deploymentQuotas.Allow(ctx, "deployment:"+endpoint.Name, endpoint.RateLimitRPM, endpoint.RateLimitTPM, tokens, time.Minute)
	if err != nil {
		release()
		return nil, &Error{Class: FailureUnavailable, Provider: endpoint.Name, StatusCode: 503, UpstreamCode: "deployment_quota_unavailable", Err: errors.Join(errors.New("deployment quota store unavailable"), err)}
	}
	if !allowed {
		release()
		return nil, &DeploymentQuotaError{Deployment: endpoint.Name, RetryAfter: retryAfter}
	}
	return release, nil
}
