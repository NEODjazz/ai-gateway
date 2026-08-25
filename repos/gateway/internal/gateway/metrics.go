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

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
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

type providerMetricKey struct {
	Endpoint  string
	Type      string
	Operation string
	Result    string
}

type moduleMetricKey struct {
	Module string
	Phase  string
	Result string
}

type cacheMetricKey struct {
	Operation string
	Result    string
}

type Metrics struct {
	mu        sync.Mutex
	values    map[metricKey]metricValue
	providers map[providerMetricKey]metricValue
	modules   map[moduleMetricKey]metricValue
	cache     map[cacheMetricKey]uint64
}

func NewMetrics() *Metrics {
	return &Metrics{
		values: map[metricKey]metricValue{}, providers: map[providerMetricKey]metricValue{},
		modules: map[moduleMetricKey]metricValue{}, cache: map[cacheMetricKey]uint64{},
	}
}

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

func (m *Metrics) ObserveProvider(endpoint, providerType, operation, result string, duration time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	key := providerMetricKey{Endpoint: endpoint, Type: providerType, Operation: operation, Result: result}
	value := m.providers[key]
	value.Count++
	value.DurationSec += duration.Seconds()
	m.providers[key] = value
	m.mu.Unlock()
}

func (m *Metrics) ObserveModule(module, phase, result string, duration time.Duration) {
	if m == nil {
		return
	}
	m.mu.Lock()
	key := moduleMetricKey{Module: module, Phase: phase, Result: result}
	value := m.modules[key]
	value.Count++
	value.DurationSec += duration.Seconds()
	m.modules[key] = value
	m.mu.Unlock()
}

func (m *Metrics) ObserveCache(operation, result string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cache[cacheMetricKey{Operation: operation, Result: result}]++
	m.mu.Unlock()
}

func (m *Metrics) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_http_requests_total Total HTTP requests.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_http_requests_total counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_http_request_duration_seconds_sum Total request duration.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_http_request_duration_seconds_sum counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_provider_attempts_total Provider calls including retries.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_provider_attempts_total counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_provider_duration_seconds_sum Total provider call duration.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_provider_duration_seconds_sum counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_module_calls_total Module calls by phase and bounded result.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_module_calls_total counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_module_duration_seconds_sum Total module call duration.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_module_duration_seconds_sum counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_cache_operations_total Exact and semantic cache operations.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_cache_operations_total counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_billing_events_total Billing module lifecycle outcomes.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_billing_events_total counter")
	_, _ = fmt.Fprintln(w, "# HELP ai_gateway_security_module_calls_total Security module outcomes.")
	_, _ = fmt.Fprintln(w, "# TYPE ai_gateway_security_module_calls_total counter")
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
	providers := make(map[providerMetricKey]metricValue, len(m.providers))
	for key, value := range m.providers {
		providers[key] = value
	}
	modules := make(map[moduleMetricKey]metricValue, len(m.modules))
	for key, value := range m.modules {
		modules[key] = value
	}
	cache := make(map[cacheMetricKey]uint64, len(m.cache))
	for key, value := range m.cache {
		cache[key] = value
	}
	m.mu.Unlock()
	for _, key := range keys {
		labels := fmt.Sprintf(`method=%q,path=%q,status=%q`, escapeMetricLabel(key.Method), escapeMetricLabel(key.Path), fmt.Sprint(key.Status))
		_, _ = fmt.Fprintf(w, "ai_gateway_http_requests_total{%s} %d\n", labels, values[key].Count)
		_, _ = fmt.Fprintf(w, "ai_gateway_http_request_duration_seconds_sum{%s} %.6f\n", labels, values[key].DurationSec)
	}
	providerKeys := make([]providerMetricKey, 0, len(providers))
	for key := range providers {
		providerKeys = append(providerKeys, key)
	}
	sort.Slice(providerKeys, func(i, j int) bool { return fmt.Sprint(providerKeys[i]) < fmt.Sprint(providerKeys[j]) })
	for _, key := range providerKeys {
		labels := fmt.Sprintf(`endpoint=%q,type=%q,operation=%q,result=%q`, escapeMetricLabel(key.Endpoint), escapeMetricLabel(key.Type), escapeMetricLabel(key.Operation), escapeMetricLabel(key.Result))
		_, _ = fmt.Fprintf(w, "ai_gateway_provider_attempts_total{%s} %d\n", labels, providers[key].Count)
		_, _ = fmt.Fprintf(w, "ai_gateway_provider_duration_seconds_sum{%s} %.6f\n", labels, providers[key].DurationSec)
	}
	moduleKeys := make([]moduleMetricKey, 0, len(modules))
	for key := range modules {
		moduleKeys = append(moduleKeys, key)
	}
	sort.Slice(moduleKeys, func(i, j int) bool { return fmt.Sprint(moduleKeys[i]) < fmt.Sprint(moduleKeys[j]) })
	for _, key := range moduleKeys {
		labels := fmt.Sprintf(`module=%q,phase=%q,result=%q`, escapeMetricLabel(key.Module), escapeMetricLabel(key.Phase), escapeMetricLabel(key.Result))
		_, _ = fmt.Fprintf(w, "ai_gateway_module_calls_total{%s} %d\n", labels, modules[key].Count)
		_, _ = fmt.Fprintf(w, "ai_gateway_module_duration_seconds_sum{%s} %.6f\n", labels, modules[key].DurationSec)
		if key.Module == "billing" {
			_, _ = fmt.Fprintf(w, "ai_gateway_billing_events_total{phase=%q,result=%q} %d\n", escapeMetricLabel(key.Phase), escapeMetricLabel(key.Result), modules[key].Count)
		}
		if key.Module == "dlp" || key.Module == "av" || key.Module == "anonymizer" {
			_, _ = fmt.Fprintf(w, "ai_gateway_security_module_calls_total{module=%q,phase=%q,result=%q} %d\n", escapeMetricLabel(key.Module), escapeMetricLabel(key.Phase), escapeMetricLabel(key.Result), modules[key].Count)
		}
	}
	cacheKeys := make([]cacheMetricKey, 0, len(cache))
	for key := range cache {
		cacheKeys = append(cacheKeys, key)
	}
	sort.Slice(cacheKeys, func(i, j int) bool { return fmt.Sprint(cacheKeys[i]) < fmt.Sprint(cacheKeys[j]) })
	for _, key := range cacheKeys {
		_, _ = fmt.Fprintf(w, "ai_gateway_cache_operations_total{operation=%q,result=%q} %d\n", escapeMetricLabel(key.Operation), escapeMetricLabel(key.Result), cache[key])
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
		path := metricPath(r.URL.Path)
		metrics.Observe(metricMethod(r.Method), path, status, duration)
		span := trace.SpanFromContext(r.Context())
		span.SetAttributes(attribute.String("ai.request.id", id), attribute.String("http.route", path))
		spanContext := span.SpanContext()
		traceID, spanID := "", ""
		if spanContext.IsValid() {
			traceID, spanID = spanContext.TraceID().String(), spanContext.SpanID().String()
		}
		payload, _ := json.Marshal(map[string]any{
			"event": "http_request", "request_id": id, "method": r.Method,
			"path": path, "status": status, "duration_ms": duration.Milliseconds(),
			"trace_id": traceID, "span_id": spanID,
		})
		log.Print(string(payload))
	})
}

func metricPath(path string) string {
	switch path {
	case "/healthz", "/readyz", "/metrics", "/openapi.yaml", "/v1/models", "/v1/chat/completions", "/v1/responses", "/v1/embeddings", "/v1/rerank":
		return path
	default:
		if path == "/docs" || strings.HasPrefix(path, "/docs/") {
			return "/docs/{asset}"
		}
		if path == "/ui" || strings.HasPrefix(path, "/ui/") {
			return "/ui/{asset}"
		}
		if strings.HasPrefix(path, "/admin/v1/keys") {
			return "/admin/v1/keys/{operation}"
		}
		if strings.HasPrefix(path, "/admin/v1/budgets") {
			return "/admin/v1/budgets/{operation}"
		}
		if strings.HasPrefix(path, "/admin/v1/model-catalog") {
			return "/admin/v1/model-catalog"
		}
		if strings.HasPrefix(path, "/admin/v1/audit/events") {
			return "/admin/v1/audit/events"
		}
		return "unmatched"
	}
}

func isInfrastructurePath(path string) bool {
	return path == "/metrics" || path == "/healthz" || path == "/readyz" ||
		path == "/openapi.yaml" || path == "/docs" || strings.HasPrefix(path, "/docs/") ||
		path == "/ui" || strings.HasPrefix(path, "/ui/")
}

func metricMethod(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost:
		return method
	default:
		return "OTHER"
	}
}
