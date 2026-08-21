package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"

	"ai-gateway-auth/internal/modules"
)

func main() {
	module := modules.NewAuthModule(true)
	defer module.Close()

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	http.HandleFunc("/livez", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	http.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := module.Ready(r.Context()); err != nil {
			http.Error(w, "auth dependencies are unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		var request authRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ctx := modules.RequestContext{APIKey: request.Token}
		if err := module.Handle(r.Context(), &ctx); err != nil {
			if errors.Is(err, modules.ErrUnauthorized) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			} else {
				http.Error(w, "authorization unavailable", http.StatusServiceUnavailable)
			}
			return
		}
		_ = json.NewEncoder(w).Encode(authResponse{
			UserID:        ctx.UserID,
			Roles:         ctx.Roles,
			CredentialID:  ctx.CredentialID,
			TeamID:        ctx.TeamID,
			AllowedModels: ctx.AllowedModels,
			AllowedTools:  ctx.AllowedTools,
			RateLimitRPM:  ctx.RateLimitRPM,
			RateLimitTPM:  ctx.RateLimitTPM,
		})
	})
	registerManagementRoutes(http.DefaultServeMux, &module, os.Getenv("MANAGEMENT_SHARED_SECRET"))

	log.Println("auth listening on :8082")
	log.Fatal(http.ListenAndServe(":8082", nil))
}

type authRequest struct {
	Token string `json:"token"`
}

type authResponse struct {
	UserID        string   `json:"user_id"`
	Roles         []string `json:"roles,omitempty"`
	CredentialID  string   `json:"credential_id,omitempty"`
	TeamID        string   `json:"team_id,omitempty"`
	AllowedModels []string `json:"allowed_models,omitempty"`
	AllowedTools  []string `json:"allowed_tools,omitempty"`
	RateLimitRPM  int      `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM  int      `json:"rate_limit_tpm,omitempty"`
}
