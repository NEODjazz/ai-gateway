package gateway

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func Routes(handler Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.Health)
	mux.HandleFunc("GET /readyz", handler.Ready)
	mux.Handle("GET /metrics", handler.metrics)
	mux.HandleFunc("GET /v1/models", handler.Models)
	mux.HandleFunc("POST /v1/chat/completions", handler.ChatCompletions)
	mux.HandleFunc("POST /v1/responses", handler.Responses)
	mux.HandleFunc("POST /v1/embeddings", handler.Embeddings)
	mux.HandleFunc("POST /admin/v1/keys", handler.CreateVirtualKey)
	mux.HandleFunc("POST /admin/v1/keys/{id}/rotate", handler.RotateVirtualKey)
	mux.HandleFunc("DELETE /admin/v1/keys/{id}", handler.RevokeVirtualKey)
	observed := observabilityMiddleware(handler.metrics, mux)
	return otelhttp.NewHandler(observed, "ai-gateway.http",
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/metrics" && r.URL.Path != "/healthz" && r.URL.Path != "/readyz"
		}),
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return metricMethod(r.Method) + " " + metricPath(r.URL.Path)
		}),
	)
}
