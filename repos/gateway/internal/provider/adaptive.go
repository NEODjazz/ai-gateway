package provider

import (
	"sort"
	"sync"
	"time"
)

type endpointRoutingStats struct {
	samples     int
	latencyEWMA float64
	failureEWMA float64
}

type adaptiveRouter struct {
	mu    sync.Mutex
	alpha float64
	stats map[string]endpointRoutingStats
}

func newAdaptiveRouter(alpha float64) *adaptiveRouter {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	return &adaptiveRouter{alpha: alpha, stats: map[string]endpointRoutingStats{}}
}

func (r *adaptiveRouter) observe(endpoint string, duration time.Duration, err error) {
	if r == nil || endpoint == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	stats := r.stats[endpoint]
	latency := float64(duration.Milliseconds())
	if latency < 1 {
		latency = 1
	}
	failure := 0.0
	if err != nil {
		failure = 1
	}
	if stats.samples == 0 {
		stats.latencyEWMA = latency
		stats.failureEWMA = failure
	} else {
		stats.latencyEWMA = r.alpha*latency + (1-r.alpha)*stats.latencyEWMA
		stats.failureEWMA = r.alpha*failure + (1-r.alpha)*stats.failureEWMA
	}
	stats.samples++
	r.stats[endpoint] = stats
}

func (r *adaptiveRouter) order(endpoints []Endpoint) []Endpoint {
	if r == nil || len(endpoints) < 2 {
		return endpoints
	}
	r.mu.Lock()
	stats := make(map[string]endpointRoutingStats, len(r.stats))
	for key, value := range r.stats {
		stats[key] = value
	}
	r.mu.Unlock()
	ordered := append([]Endpoint(nil), endpoints...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, leftFound := stats[ordered[i].Name]
		right, rightFound := stats[ordered[j].Name]
		if !leftFound || left.samples == 0 {
			return rightFound && right.samples > 0
		}
		if !rightFound || right.samples == 0 {
			return false
		}
		leftScore := left.latencyEWMA * (1 + 4*left.failureEWMA)
		rightScore := right.latencyEWMA * (1 + 4*right.failureEWMA)
		return leftScore < rightScore
	})
	return ordered
}
