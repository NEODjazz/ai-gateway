package gateway

import "net/http"

func Routes(handler Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handler.Health)
	mux.HandleFunc("GET /v1/models", handler.Models)
	mux.HandleFunc("POST /v1/chat/completions", handler.ChatCompletions)
	mux.HandleFunc("POST /v1/responses", handler.Responses)
	return mux
}
