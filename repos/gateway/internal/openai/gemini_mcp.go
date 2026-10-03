package openai

import "regexp"

const MaxGeminiMCPServers = 8

var geminiMCPServerIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

type GeminiMCPServer struct {
	Name                    string                        `json:"name"`
	StreamableHTTPTransport GeminiStreamableHTTPTransport `json:"streamableHttpTransport"`
}

type GeminiStreamableHTTPTransport struct {
	URL              string            `json:"url"`
	Headers          map[string]string `json:"headers,omitempty"`
	Timeout          string            `json:"timeout,omitempty"`
	SSEReadTimeout   string            `json:"sseReadTimeout,omitempty"`
	TerminateOnClose bool              `json:"terminateOnClose,omitempty"`
}

func ValidGeminiMCPServerIDs(values []string) bool {
	if len(values) == 0 || len(values) > MaxGeminiMCPServers {
		return false
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !geminiMCPServerIDPattern.MatchString(value) || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}
