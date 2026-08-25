package provider

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

var ErrCircuitOpen = errors.New("provider circuit is open")

type CircuitStore interface {
	CircuitAvailable(ctx context.Context, endpoint string, now time.Time) (bool, error)
	CircuitPermit(ctx context.Context, endpoint string, now time.Time, probeTTL time.Duration) (bool, error)
	CircuitSuccess(ctx context.Context, endpoint string) error
	CircuitFailure(ctx context.Context, endpoint string, threshold int, cooldown time.Duration, now time.Time) error
}

type endpointHealthState struct {
	failures      int
	cooldownUntil time.Time
	probeUntil    time.Time
}

type endpointHealthTracker struct {
	mu       sync.Mutex
	states   map[string]endpointHealthState
	now      func() time.Time
	store    CircuitStore
	probeTTL time.Duration
}

func newEndpointHealthTracker(stores ...CircuitStore) *endpointHealthTracker {
	var store CircuitStore
	if len(stores) > 0 && !interfaceIsNil(stores[0]) {
		store = stores[0]
	}
	return &endpointHealthTracker{
		states: map[string]endpointHealthState{}, now: time.Now, store: store, probeTTL: 30 * time.Second,
	}
}

func (h *endpointHealthTracker) available(ctx context.Context, endpoint Endpoint) bool {
	if h == nil {
		return true
	}
	now := h.now()
	if h.store != nil {
		available, err := h.store.CircuitAvailable(ctx, endpoint.Name, now)
		if err == nil {
			return available
		}
		log.Printf("distributed circuit availability failed for endpoint %q: %v", endpoint.Name, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[endpoint.Name]
	return state.cooldownUntil.IsZero() || !now.Before(state.cooldownUntil)
}

func (h *endpointHealthTracker) localState(endpoint string) endpointHealthState {
	if h == nil {
		return endpointHealthState{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.states[endpoint]
}

func (h *endpointHealthTracker) permit(ctx context.Context, endpoint Endpoint) error {
	if h == nil {
		return nil
	}
	now := h.now()
	if h.store != nil {
		allowed, err := h.store.CircuitPermit(ctx, endpoint.Name, now, h.probeTTL)
		if err == nil {
			if allowed {
				return nil
			}
			return ErrCircuitOpen
		}
		log.Printf("distributed circuit permit failed for endpoint %q: %v", endpoint.Name, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[endpoint.Name]
	if state.cooldownUntil.IsZero() {
		return nil
	}
	if now.Before(state.cooldownUntil) || now.Before(state.probeUntil) {
		return ErrCircuitOpen
	}
	state.probeUntil = now.Add(h.probeTTL)
	h.states[endpoint.Name] = state
	return nil
}

func (h *endpointHealthTracker) success(ctx context.Context, endpoint Endpoint) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.states, endpoint.Name)
	h.mu.Unlock()
	if h.store != nil {
		if err := h.store.CircuitSuccess(ctx, endpoint.Name); err != nil {
			log.Printf("distributed circuit success failed for endpoint %q: %v", endpoint.Name, err)
		}
	}
}

func (h *endpointHealthTracker) failure(ctx context.Context, endpoint Endpoint, err error) {
	if h == nil || !shouldCooldown(err) {
		return
	}
	threshold := endpoint.CooldownAfterFailures
	if threshold <= 0 {
		threshold = 3
	}
	duration := endpoint.Cooldown
	if duration <= 0 {
		duration = 30 * time.Second
	}
	now := h.now()
	h.mu.Lock()
	state := h.states[endpoint.Name]
	if !state.cooldownUntil.IsZero() {
		state.failures = 0
		state.cooldownUntil = now.Add(duration)
		state.probeUntil = time.Time{}
	} else {
		state.failures++
		if state.failures >= threshold {
			state.cooldownUntil = now.Add(duration)
			state.probeUntil = time.Time{}
			state.failures = 0
		}
	}
	h.states[endpoint.Name] = state
	h.mu.Unlock()
	if h.store != nil {
		if storeErr := h.store.CircuitFailure(ctx, endpoint.Name, threshold, duration, now); storeErr != nil {
			log.Printf("distributed circuit failure update failed for endpoint %q: %v", endpoint.Name, storeErr)
		}
	}
}
