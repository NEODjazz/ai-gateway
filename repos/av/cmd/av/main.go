package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"ai-gateway-av/internal/modules"
)

func main() {
	client := modules.NewICAPClient(
		env("AV_ICAP_HOST", env("ICAP_HOST", "")),
		env("AV_ICAP_PORT", env("ICAP_PORT", "")),
		env("AV_ICAP_SERVICE", env("ICAP_SERVICE", "/av")),
	)
	client.Timeout = envDuration("AV_ICAP_TIMEOUT", envDuration("ICAP_TIMEOUT", 5*time.Second))

	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	http.HandleFunc("/scan", func(w http.ResponseWriter, r *http.Request) {
		var req scanRequest
		r.Body = http.MaxBytesReader(w, r.Body, 24<<20)
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if strings.TrimSpace(req.Content) != "" {
			if _, err := client.Scan(r.Context(), "av", []byte(req.Content)); err != nil {
				writeScanError(w, err)
				return
			}
		}
		if len(req.Attachments) > 8 {
			http.Error(w, "too many attachments", http.StatusRequestEntityTooLarge)
			return
		}
		totalBytes := 0
		for _, attachment := range req.Attachments {
			if !allowedImageMediaType(attachment.MediaType) {
				http.Error(w, "unsupported attachment media type", http.StatusBadRequest)
				return
			}
			if base64.StdEncoding.DecodedLen(len(attachment.Data)) > 8<<20 {
				http.Error(w, "attachment is too large", http.StatusRequestEntityTooLarge)
				return
			}
			payload, err := base64.StdEncoding.DecodeString(attachment.Data)
			if err != nil || len(payload) == 0 {
				http.Error(w, "invalid attachment", http.StatusBadRequest)
				return
			}
			if !validImageSignature(attachment.MediaType, payload) {
				http.Error(w, "attachment bytes do not match media type", http.StatusBadRequest)
				return
			}
			totalBytes += len(payload)
			if totalBytes > 16<<20 {
				http.Error(w, "attachments are too large", http.StatusRequestEntityTooLarge)
				return
			}
			if _, err := client.ScanContent(r.Context(), "av", attachment.MediaType, payload); err != nil {
				writeScanError(w, err)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(scanResponse{Allowed: true})
	})

	addr := env("HTTP_ADDR", ":8085")
	log.Printf("av listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func writeScanError(w http.ResponseWriter, err error) {
	if errors.Is(err, modules.ErrContentRejected) {
		http.Error(w, err.Error(), http.StatusUnavailableForLegalReasons)
		return
	}
	http.Error(w, err.Error(), http.StatusBadGateway)
}

type scanRequest struct {
	RequestID   string           `json:"request_id,omitempty"`
	Content     string           `json:"content"`
	Attachments []scanAttachment `json:"attachments,omitempty"`
}

type scanAttachment struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data_base64"`
}

type scanResponse struct {
	Allowed bool `json:"allowed"`
}

func allowedImageMediaType(value string) bool {
	switch value {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}

func validImageSignature(mediaType string, data []byte) bool {
	switch mediaType {
	case "image/jpeg":
		return len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff})
	case "image/png":
		return len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n"))
	case "image/gif":
		return len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a")))
	case "image/webp":
		return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP"))
	default:
		return false
	}
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
