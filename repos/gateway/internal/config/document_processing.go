package config

import "time"

type DoclingConfig struct {
	URL          string
	APIKey       string
	Timeout      time.Duration
	PollInterval time.Duration
	MaxTextBytes int
}

// ValidDocumentProcessing validates the gateway processing policy separately
// from the native capabilities advertised by a provider adapter.
func ValidDocumentProcessing(mode string) bool {
	return mode == "" || mode == "native" || mode == "docling"
}
