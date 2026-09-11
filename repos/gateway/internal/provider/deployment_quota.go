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
	AllowMany(context.Context, []string, []int, []int, int, time.Duration) (bool, int, time.Duration, error)
}

type DeploymentQuotaError struct {
	Deployment string
	RetryAfter time.Duration
}

func (e *DeploymentQuotaError) Error() string {
	return e.Deployment + " deployment rate limit exceeded"
}

type ProviderQuotaError struct {
	Provider   string
	RetryAfter time.Duration
}

func (e *ProviderQuotaError) Error() string {
	return e.Provider + " provider rate limit exceeded"
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
	allowed, _, retryAfter, err := s.AllowMany(ctx, []string{key}, []int{requestLimit}, []int{tokenLimit}, tokens, window)
	return allowed, retryAfter, err
}

func (s *MemoryDeploymentQuotaStore) AllowMany(ctx context.Context, keys []string, requestLimits, tokenLimits []int, tokens int, window time.Duration) (bool, int, time.Duration, error) {
	if s == nil {
		return false, -1, 0, errors.New("memory deployment quota store is not configured")
	}
	if len(keys) == 0 || len(keys) != len(requestLimits) || len(keys) != len(tokenLimits) || len(keys) > 16 {
		return false, -1, 0, errors.New("invalid deployment quota scopes")
	}
	if err := ctx.Err(); err != nil {
		return false, -1, 0, err
	}
	if window <= 0 {
		window = time.Minute
	}
	tokens = max(tokens, 0)
	identities := make([][32]byte, len(keys))
	seen := make(map[[32]byte]bool, len(keys))
	for index, key := range keys {
		if key == "" || requestLimits[index] <= 0 && tokenLimits[index] <= 0 {
			return false, -1, 0, errors.New("invalid deployment quota scope")
		}
		identities[index] = sha256.Sum256([]byte(key))
		if seen[identities[index]] {
			return false, -1, 0, errors.New("duplicate deployment quota scope")
		}
		seen[identities[index]] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	newScopes := 0
	for _, identity := range identities {
		current, found := s.windows[identity]
		if found && now.Sub(current.started) >= window {
			delete(s.windows, identity)
			found = false
		}
		if !found {
			newScopes++
		}
	}
	if len(s.windows)+newScopes > memoryDeploymentQuotaCapacity {
		for candidate, value := range s.windows {
			if now.Sub(value.started) >= window {
				delete(s.windows, candidate)
			}
		}
	}
	if len(s.windows)+newScopes > memoryDeploymentQuotaCapacity {
		return false, -1, 0, errors.New("memory deployment quota capacity exceeded")
	}
	currents := make([]deploymentQuotaWindow, len(identities))
	for index, identity := range identities {
		current, found := s.windows[identity]
		if !found {
			current.started = now
		}
		if requestLimits[index] > 0 && current.requests >= requestLimits[index] ||
			tokenLimits[index] > 0 && (current.tokens > tokenLimits[index] || tokens > tokenLimits[index]-current.tokens) {
			return false, index, max(window-now.Sub(current.started), time.Millisecond), nil
		}
		currents[index] = current
	}
	for index, identity := range identities {
		currents[index].requests = saturatedQuotaCount(currents[index].requests, 1)
		currents[index].tokens = saturatedQuotaCount(currents[index].tokens, tokens)
		s.windows[identity] = currents[index]
	}
	return true, -1, 0, nil
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
	if err := r.reserveEndpointQuota(ctx, endpoint, tokens); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func (r Router) reserveEndpointQuota(ctx context.Context, endpoint Endpoint, tokens int) error {
	if endpoint.RateLimitRPM <= 0 && endpoint.RateLimitTPM <= 0 && endpoint.ProviderRateLimitRPM <= 0 && endpoint.ProviderRateLimitTPM <= 0 {
		return nil
	}
	if r.deploymentQuotas == nil {
		return errors.New("deployment quota store unavailable")
	}
	keys := make([]string, 0, 2)
	requestLimits := make([]int, 0, 2)
	tokenLimits := make([]int, 0, 2)
	scopes := make([]string, 0, 2)
	if endpoint.ProviderRateLimitRPM > 0 || endpoint.ProviderRateLimitTPM > 0 {
		keys = append(keys, "provider:"+endpoint.ProviderID)
		requestLimits = append(requestLimits, endpoint.ProviderRateLimitRPM)
		tokenLimits = append(tokenLimits, endpoint.ProviderRateLimitTPM)
		scopes = append(scopes, "provider")
	}
	if endpoint.RateLimitRPM > 0 || endpoint.RateLimitTPM > 0 {
		keys = append(keys, "deployment:"+endpoint.Name)
		requestLimits = append(requestLimits, endpoint.RateLimitRPM)
		tokenLimits = append(tokenLimits, endpoint.RateLimitTPM)
		scopes = append(scopes, "deployment")
	}
	allowed, rejected, retryAfter, err := r.deploymentQuotas.AllowMany(ctx, keys, requestLimits, tokenLimits, tokens, time.Minute)
	if err != nil {
		return &Error{Class: FailureUnavailable, Provider: endpoint.Name, StatusCode: 503, UpstreamCode: "deployment_quota_unavailable", Err: errors.Join(errors.New("deployment quota store unavailable"), err)}
	}
	if !allowed {
		if rejected >= 0 && rejected < len(scopes) && scopes[rejected] == "provider" {
			return &ProviderQuotaError{Provider: endpoint.ProviderID, RetryAfter: retryAfter}
		}
		return &DeploymentQuotaError{Deployment: endpoint.Name, RetryAfter: retryAfter}
	}
	return nil
}
