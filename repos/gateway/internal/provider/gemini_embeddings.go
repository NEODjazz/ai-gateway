package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"ai-gateway-gateway/internal/openai"
)

const maxGeminiEmbeddingInputs = 100

func (Gemini) ValidateEmbeddingParameters(request openai.EmbeddingRequest) error {
	if err := rejectParameters("gemini", parameterCheck{"metadata", request.Metadata != nil}, parameterCheck{"input_type", request.InputType != ""}, parameterCheck{"user", request.User != ""}, parameterCheck{"encoding_format", request.EncodingFormat != "" && request.EncodingFormat != "float"}); err != nil {
		return err
	}
	input, err := openai.InspectEmbeddingInput(request.Input)
	if err != nil {
		return geminiInvalid("input")
	}
	if input.Tokenized() {
		return rejectParameters("gemini", parameterCheck{"input", true})
	}
	inputs, ok := openai.EmbeddingInputStrings(request.Input)
	if !ok || len(inputs) > maxGeminiEmbeddingInputs {
		return geminiInvalid("input")
	}
	if request.Dimensions != nil && (*request.Dimensions <= 0 || *request.Dimensions > 65536) {
		return geminiInvalid("dimensions")
	}
	model := strings.TrimPrefix(request.Model, "models/")
	if strings.TrimSpace(model) == "" || strings.ContainsAny(model, "/\\?#%") || model == "." || model == ".." {
		return geminiInvalid("model")
	}
	return nil
}

func (g Gemini) Embeddings(ctx context.Context, request openai.EmbeddingRequest) (openai.EmbeddingResponse, error) {
	if err := g.ValidateEmbeddingParameters(request); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	model := strings.TrimPrefix(request.Model, "models/")
	base, err := url.Parse(g.baseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return openai.EmbeddingResponse{}, errors.New("invalid Gemini base URL")
	}
	inputs, _ := openai.EmbeddingInputStrings(request.Input)
	type entry struct {
		Model      string        `json:"model"`
		Content    geminiContent `json:"content"`
		Dimensions *int          `json:"outputDimensionality,omitempty"`
	}
	native := struct {
		Requests []entry `json:"requests"`
	}{Requests: make([]entry, len(inputs))}
	for i, text := range inputs {
		native.Requests[i] = entry{Model: "models/" + model, Content: geminiContent{Parts: []geminiPart{{Text: text}}}, Dimensions: request.Dimensions}
	}
	body, err := json.Marshal(native)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if len(body) > openai.MaxInferenceBodyBytes {
		return openai.EmbeddingResponse{}, geminiInvalid("input")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	endpoint := geminiBaseURL(g.baseURL) + "/models/" + url.PathEscape(model) + ":batchEmbedContents"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", g.apiKey)
	response, err := g.client.Do(req)
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return openai.EmbeddingResponse{}, responseStatusError("gemini", response)
	}
	const maxResponseBytes = 32 << 20
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if len(payload) > maxResponseBytes {
		return openai.EmbeddingResponse{}, errors.New("Gemini embedding response exceeds limit")
	}
	var upstream struct {
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
		Usage *struct {
			PromptTokens *int `json:"promptTokenCount"`
		} `json:"usageMetadata"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(payload, &upstream); err != nil {
		return openai.EmbeddingResponse{}, err
	}
	if len(upstream.Error) > 0 || len(upstream.Embeddings) != len(inputs) {
		return openai.EmbeddingResponse{}, errors.New("invalid Gemini embedding response")
	}
	result := openai.EmbeddingResponse{Object: "list", Model: request.Model, Data: make([]openai.Embedding, len(inputs))}
	dimensions := 0
	for i, embedding := range upstream.Embeddings {
		if i == 0 {
			dimensions = len(embedding.Values)
		}
		if dimensions == 0 || dimensions > 65536 || len(embedding.Values) != dimensions || (request.Dimensions != nil && dimensions != *request.Dimensions) {
			return openai.EmbeddingResponse{}, errors.New("invalid Gemini embedding dimensions")
		}
		for _, value := range embedding.Values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return openai.EmbeddingResponse{}, errors.New("invalid Gemini embedding value")
			}
		}
		result.Data[i] = openai.Embedding{Object: "embedding", Index: i, Embedding: embedding.Values}
	}
	tokens := openai.EstimateContextTokens(request.Input)
	if upstream.Usage != nil {
		if upstream.Usage.PromptTokens == nil || *upstream.Usage.PromptTokens < 0 {
			return openai.EmbeddingResponse{}, errors.New("invalid Gemini embedding usage")
		}
		tokens = *upstream.Usage.PromptTokens
		result.UsageReported = true
	}
	result.Usage = openai.Usage{PromptTokens: tokens, TotalTokens: tokens}
	return result, nil
}
