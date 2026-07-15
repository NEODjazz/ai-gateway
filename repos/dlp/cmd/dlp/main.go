package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"ai-gateway-dlp/internal/modules"
)

func main() {
	client := modules.NewICAPClient(
		env("DLP_ICAP_HOST", env("ICAP_HOST", "")),
		env("DLP_ICAP_PORT", env("ICAP_PORT", "")),
		env("DLP_ICAP_SERVICE", env("ICAP_SERVICE", "/dlp")),
	)
	client.Timeout = envDuration("DLP_ICAP_TIMEOUT", envDuration("ICAP_TIMEOUT", 5*time.Second))

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/scan", func(w http.ResponseWriter, r *http.Request) {
		var req modules.RequestContext
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		payload := modules.ScanPayload(&req)
		if strings.TrimSpace(payload) == "" {
			_ = json.NewEncoder(w).Encode(req)
			return
		}

		if _, err := client.Scan(r.Context(), "dlp", []byte(payload)); err != nil {
			if errors.Is(err, modules.ErrContentRejected) {
				http.Error(w, err.Error(), http.StatusUnavailableForLegalReasons)
				return
			}
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(req)
	})

	addr := env("HTTP_ADDR", ":8084")
	log.Printf("dlp listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return duration
}
