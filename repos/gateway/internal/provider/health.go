package provider

import (
	"sync"
	"time"
)

type endpointHealthState struct {
	failures      int
	cooldownUntil time.Time
}

type endpointHealthTracker struct {
	mu     sync.Mutex
	states map[string]endpointHealthState
	now    func() time.Time
}

func newEndpointHealthTracker() *endpointHealthTracker {
	return &endpointHealthTracker{states: map[string]endpointHealthState{}, now: time.Now}
}

func (h *endpointHealthTracker) available(endpoint Endpoint) bool {
	if h == nil {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	state := h.states[endpoint.Name]
	return state.cooldownUntil.IsZero() || !h.now().Before(state.cooldownUntil)
}

func (h *endpointHealthTracker) success(endpoint Endpoint) {
	if h == nil {
		return
	}
	h.mu.Lock()
	delete(h.states, endpoint.Name)
	h.mu.Unlock()
}

func (h *endpointHealthTracker) failure(endpoint Endpoint, err error) {
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
	h.mu.Lock()
	state := h.states[endpoint.Name]
	state.failures++
	if state.failures >= threshold {
		state.cooldownUntil = h.now().Add(duration)
		state.failures = 0
	}
	h.states[endpoint.Name] = state
	h.mu.Unlock()
}
