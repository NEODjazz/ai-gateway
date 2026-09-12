package openai

import (
	"strings"
)

const (
	DefaultSandboxTemplate       = "opensandbox/code-interpreter:v1.1.0"
	DefaultSandboxLanguage       = "python"
	DefaultSandboxTimeoutSeconds = 300
	MaxSandboxCodeBytes          = 1 << 20
)

type SandboxExecuteRequest struct {
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model"`
	Code                string `json:"code"`
	Language            string `json:"language,omitempty"`
	Template            string `json:"template,omitempty"`
	TimeoutSeconds      int    `json:"timeout_seconds,omitempty"`
	AllowInternetAccess bool   `json:"allow_internet_access,omitempty"`
}

type SandboxExecutionResult struct {
	Stdout         string           `json:"stdout"`
	Stderr         string           `json:"stderr"`
	Results        []map[string]any `json:"results"`
	Error          map[string]any   `json:"error,omitempty"`
	ExecutionCount *int             `json:"execution_count,omitempty"`
	Object         string           `json:"object"`
}

func (r *SandboxExecuteRequest) ApplyDefaults() {
	if r.Language == "" {
		r.Language = DefaultSandboxLanguage
	}
	if r.Template == "" {
		r.Template = DefaultSandboxTemplate
	}
	if r.TimeoutSeconds == 0 {
		r.TimeoutSeconds = DefaultSandboxTimeoutSeconds
	}
}

func (r SandboxExecuteRequest) Validate() string {
	if r.Model == "" || len(r.Model) > 256 || r.Provider != "" && len(r.Provider) > 128 || r.Code == "" || len(r.Code) > MaxSandboxCodeBytes || r.Language == "" || len(r.Language) > 32 || r.Template == "" || len(r.Template) > 512 || r.TimeoutSeconds < 1 || r.TimeoutSeconds > 900 {
		return "invalid sandbox execution request"
	}
	for _, value := range []string{r.Model, r.Provider, r.Language, r.Template} {
		if strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n\x00") {
			return "invalid sandbox execution request"
		}
	}
	return ""
}

func (r SandboxExecuteRequest) InputTokens() int {
	return EstimateContextTokens(struct {
		Code     string `json:"code"`
		Language string `json:"language"`
		Template string `json:"template"`
	}{r.Code, r.Language, r.Template})
}
