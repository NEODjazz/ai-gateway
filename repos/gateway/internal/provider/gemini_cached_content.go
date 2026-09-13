package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const maxGeminiCachedContentMetadataBytes = 1 << 20

type GeminiCachedContentClient interface {
	CreateCachedContent(context.Context, openai.ChatCompletionRequest, string, openai.GeminiCachedContentExpiration) (openai.GeminiCachedContent, error)
	GetCachedContent(context.Context, string) (openai.GeminiCachedContent, error)
	UpdateCachedContent(context.Context, string, openai.GeminiCachedContentExpiration) (openai.GeminiCachedContent, error)
	DeleteCachedContent(context.Context, string) error
}

func (g Gemini) CreateCachedContent(ctx context.Context, request openai.ChatCompletionRequest, displayName string, expiration openai.GeminiCachedContentExpiration) (openai.GeminiCachedContent, error) {
	var result openai.GeminiCachedContent
	if !validGeminiCachedContentDisplayName(displayName) || !validGeminiCachedContentExpiration(expiration) {
		return result, geminiInvalid("cachedContent")
	}
	native, err := geminiChatRequest(request)
	if err != nil {
		return result, err
	}
	generation, err := json.Marshal(native.Generation)
	if err != nil || string(generation) != "{}" || request.Stream || native.ServiceTier != "" || native.Store != nil || len(native.Safety) != 0 || len(native.Contents) == 0 && native.System == nil && len(native.Tools) == 0 {
		return result, geminiInvalid("cachedContent")
	}
	model := strings.TrimPrefix(request.Model, "models/")
	if !validGeminiModelName(model) {
		return result, geminiInvalid("model")
	}
	body := struct {
		Contents    []geminiContent `json:"contents,omitempty"`
		System      *geminiContent  `json:"systemInstruction,omitempty"`
		Tools       []geminiTool    `json:"tools,omitempty"`
		ToolConfig  map[string]any  `json:"toolConfig,omitempty"`
		TTL         string          `json:"ttl,omitempty"`
		ExpireTime  string          `json:"expireTime,omitempty"`
		DisplayName string          `json:"displayName,omitempty"`
		Model       string          `json:"model"`
	}{
		Contents: native.Contents, System: native.System, Tools: native.Tools,
		ToolConfig: native.ToolConfig, TTL: expiration.TTL, ExpireTime: expiration.ExpireTime,
		DisplayName: displayName, Model: "models/" + model,
	}
	if err := g.cachedContentJSON(ctx, http.MethodPost, "cachedContents", "", body, &result); err != nil {
		return openai.GeminiCachedContent{}, err
	}
	if err := validateGeminiCachedContent(result); err != nil {
		return openai.GeminiCachedContent{}, err
	}
	return result, nil
}

func (g Gemini) GetCachedContent(ctx context.Context, name string) (openai.GeminiCachedContent, error) {
	var result openai.GeminiCachedContent
	if !validGeminiCachedContentName(name) {
		return result, geminiInvalid("cachedContent")
	}
	if err := g.cachedContentJSON(ctx, http.MethodGet, name, "", nil, &result); err != nil {
		return openai.GeminiCachedContent{}, err
	}
	if err := validateGeminiCachedContent(result); err != nil || result.Name != name {
		if err == nil {
			err = errors.New("Gemini returned a different cached content resource")
		}
		return openai.GeminiCachedContent{}, err
	}
	return result, nil
}

func (g Gemini) UpdateCachedContent(ctx context.Context, name string, expiration openai.GeminiCachedContentExpiration) (openai.GeminiCachedContent, error) {
	var result openai.GeminiCachedContent
	if !validGeminiCachedContentName(name) || !validGeminiCachedContentExpiration(expiration) {
		return result, geminiInvalid("cachedContent")
	}
	mask := "ttl"
	if expiration.ExpireTime != "" {
		mask = "expireTime"
	}
	body := struct {
		Name       string `json:"name"`
		TTL        string `json:"ttl,omitempty"`
		ExpireTime string `json:"expireTime,omitempty"`
	}{name, expiration.TTL, expiration.ExpireTime}
	if err := g.cachedContentJSON(ctx, http.MethodPatch, name, "updateMask="+url.QueryEscape(mask), body, &result); err != nil {
		return openai.GeminiCachedContent{}, err
	}
	if err := validateGeminiCachedContent(result); err != nil || result.Name != name {
		if err == nil {
			err = errors.New("Gemini returned a different cached content resource")
		}
		return openai.GeminiCachedContent{}, err
	}
	return result, nil
}

func (g Gemini) DeleteCachedContent(ctx context.Context, name string) error {
	if !validGeminiCachedContentName(name) {
		return geminiInvalid("cachedContent")
	}
	return g.cachedContentJSON(ctx, http.MethodDelete, name, "", nil, nil)
}

func (g Gemini) cachedContentJSON(ctx context.Context, method, path, rawQuery string, input, output any) error {
	var body io.Reader = http.NoBody
	if input != nil {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(payload)
	}
	base, err := url.Parse(g.baseURL)
	if err != nil || base.Scheme != "https" && base.Scheme != "http" || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return errors.New("invalid Gemini base URL")
	}
	endpoint := geminiBaseURL(g.baseURL) + "/" + path
	if rawQuery != "" {
		endpoint += "?" + rawQuery
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if err := g.authorize(request); err != nil {
		return err
	}
	response, err := g.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError("gemini", response)
	}
	if output == nil {
		read, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxGeminiCachedContentMetadataBytes+1))
		if err != nil {
			return err
		}
		if read > maxGeminiCachedContentMetadataBytes {
			return errors.New("Gemini cached content response exceeds limit")
		}
		return nil
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxGeminiCachedContentMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxGeminiCachedContentMetadataBytes {
		return errors.New("Gemini cached content response exceeds limit")
	}
	if json.Unmarshal(payload, output) != nil {
		return errors.New("invalid Gemini cached content response")
	}
	return nil
}

func validGeminiCachedContentName(name string) bool {
	if !strings.HasPrefix(name, "cachedContents/") || len(name) > 256 {
		return false
	}
	id := strings.TrimPrefix(name, "cachedContents/")
	return id != "" && !strings.ContainsAny(id, "/\\?#%") && id != "." && id != ".."
}

func validGeminiCachedContentDisplayName(name string) bool {
	return utf8.ValidString(name) && utf8.RuneCountInString(name) <= 128
}

func validGeminiCachedContentExpiration(expiration openai.GeminiCachedContentExpiration) bool {
	if (expiration.TTL == "") == (expiration.ExpireTime == "") {
		return false
	}
	if expiration.TTL != "" {
		if !strings.HasSuffix(expiration.TTL, "s") || strings.ContainsAny(strings.TrimSuffix(expiration.TTL, "s"), "+-") {
			return false
		}
		duration, err := time.ParseDuration(expiration.TTL)
		return err == nil && duration > 0
	}
	_, err := time.Parse(time.RFC3339Nano, expiration.ExpireTime)
	return err == nil
}

func validGeminiModelName(model string) bool {
	return model != "" && len(model) <= 256 && !strings.ContainsAny(model, "/\\?#%") && model != "." && model != ".."
}

func validateGeminiCachedContent(content openai.GeminiCachedContent) error {
	if !validGeminiCachedContentName(content.Name) || !strings.HasPrefix(content.Model, "models/") || !validGeminiModelName(strings.TrimPrefix(content.Model, "models/")) || !validGeminiCachedContentDisplayName(content.DisplayName) || content.UsageMetadata != nil && content.UsageMetadata.TotalTokenCount < 0 {
		return errors.New("invalid Gemini cached content metadata")
	}
	for _, value := range []string{content.CreateTime, content.UpdateTime, content.ExpireTime} {
		if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
			return errors.New("invalid Gemini cached content timestamp")
		}
	}
	return nil
}
