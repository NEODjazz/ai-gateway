package gateway

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type metricKey struct {
	Path   string
	Method string
	Status int
}

type metricValue struct {
	Count       uint64
	DurationSec float64
}

type Metrics struct {
	mu     sync.Mutex
	values map[metricKey]metricValue
}

func NewMetrics() *Metrics { return &Metrics{values: map[metricKey]metricValue{}} }

func (m *Metrics) Observe(method, path string, status int, duration time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	key := metricKey{Path: path, Method: method, Status: status}
	value := m.values[key]
	value.Count++
	value.DurationSec += duration.Seconds()
	m.values[key] = value
	m.mu.Unlock()
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_http_requests_total Total HTTP requests.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_http_requests_total counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_http_request_duration_seconds_sum Total request duration.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_http_request_duration_seconds_sum counter")
	m.mu.Lock()
	keys := make([]metricKey, 0, len(m.values))
	for key := range m.values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].Path+keys[i].Method+fmt.Sprint(keys[i].Status) < keys[j].Path+keys[j].Method+fmt.Sprint(keys[j].Status)
	})
	values := make(map[metricKey]metricValue, len(m.values))
	for key, value := range m.values {
		values[key] = value
	}
	m.mu.Unlock()
	for _, key := range keys {
		labels := fmt.Sprintf(`method=%q,path=%q,status=%q`, escapeMetricLabel(key.Method), escapeMetricLabel(key.Path), fmt.Sprint(key.Status))
		_, _ = fmt.Fprintf(w, "ai_gateway_http_requests_total{%s} %d\n", labels, values[key].Count)
		_, _ = fmt.Fprintf(w, "ai_gateway_http_request_duration_seconds_sum{%s} %.6f\n", labels, values[key].DurationSec)
	}
}

func escapeMetricLabel(value string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", `"`, `\"`).Replace(value)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusRecorder) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func observabilityMiddleware(metrics *Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		id := requestID(r)
		r.Header.Set("X-Request-ID", id)
		w.Header().Set("X-Request-ID", id)
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		duration := time.Since(started)
		metrics.Observe(r.Method, r.URL.Path, status, duration)
		payload, _ := json.Marshal(map[string]any{
			"event": "http_request", "request_id": id, "method": r.Method,
			"path": r.URL.Path, "status": status, "duration_ms": duration.Milliseconds(),
		})
		log.Print(string(payload))
	})
}
