package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/vectorstate"
)

type ragQueryAuthModule struct {
	calls int
	user  string
}

func (*ragQueryAuthModule) Name() string   { return "auth" }
func (*ragQueryAuthModule) Required() bool { return true }
func (m *ragQueryAuthModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	req.CredentialID = "credential"
	req.UserID = m.user
	req.AllowedModels = []string{"chat-model", "embed-model", "rerank-model"}
	req.RateLimitTPM = 100000
	return nil
}

type ragQueryProvider struct {
	modelsProvider
	embeddingRequest modules.RequestContext
	rerankRequest    modules.RequestContext
	chatRequest      modules.RequestContext
	streamRequest    modules.RequestContext
	stream           bool
	streamFail       bool
}

func (p *ragQueryProvider) Embeddings(_ context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error) {
	p.embeddingRequest = req
	inputs := req.EmbeddingRequest.Input.([]string)
	data := make([]openai.Embedding, len(inputs))
	for index, input := range inputs {
		vector := []float64{0, 1}
		if input == "alpha" || strings.Contains(input, "alpha") {
			vector = []float64{1, 0}
		}
		data[index] = openai.Embedding{Object: "embedding", Embedding: vector, Index: index}
	}
	return openai.EmbeddingResponse{UsageReported: true, Object: "list", Model: req.EmbeddingRequest.Model, Data: data, Usage: openai.Usage{PromptTokens: len(inputs), TotalTokens: len(inputs)}}, nil
}

func (p *ragQueryProvider) Rerank(_ context.Context, req modules.RequestContext) (openai.RerankResponse, error) {
	p.rerankRequest = req
	return openai.RerankResponse{ID: "rerank-rag", Results: []openai.RerankResult{{Index: 1, RelevanceScore: 0.99}}}, nil
}

func (p *ragQueryProvider) ChatCompletions(_ context.Context, req modules.RequestContext) (openai.ChatCompletionResponse, error) {
	p.chatRequest = req
	return openai.ChatCompletionResponse{
		ID: "chatcmpl-rag", Object: "chat.completion", Model: req.Request.Model,
		Choices: []openai.Choice{{Index: 0, Message: openai.Message{Role: "assistant", Content: "answer [1]"}, FinishReason: "stop"}},
		Usage:   openai.Usage{PromptTokens: 12, CompletionTokens: 1, TotalTokens: 13},
	}, nil
}

func (p *ragQueryProvider) StreamChatCompletions(_ context.Context, req modules.RequestContext, write provider.ChatCompletionStreamWriter) (openai.ChatCompletionResponse, bool, error) {
	if !p.stream {
		return openai.ChatCompletionResponse{}, false, nil
	}
	p.streamRequest = req
	response := openai.ChatCompletionResponse{ID: "chatcmpl-rag-stream", Object: "chat.completion", Model: req.Request.Model, Usage: openai.Usage{PromptTokens: 10, CompletionTokens: 1, TotalTokens: 11}}
	if err := write(`{"id":"chatcmpl-rag-stream","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{"content":"answer ["},"finish_reason":null}]}`); err != nil {
		return openai.ChatCompletionResponse{}, true, err
	}
	if p.streamFail {
		return response, true, errors.New("upstream stream failed")
	}
	if err := write(`{"id":"chatcmpl-rag-stream","object":"chat.completion.chunk","model":"chat-model","choices":[{"index":0,"delta":{"content":"1]"},"finish_reason":"stop"}]}`); err != nil {
		return openai.ChatCompletionResponse{}, true, err
	}
	return response, true, nil
}

func TestRAGQueryStreamsChatCompletion(t *testing.T) {
	runtime := &ragQueryProvider{stream: true}
	_, handler := newRAGQueryHandler(t, "user", runtime)
	request := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader(`{"model":"chat-model","messages":[{"role":"user","content":"alpha"}],"stream":true,"stream_options":{"include_usage":true},"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model","top_k":1}}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(response.Body.String(), "chatcmpl-rag-stream") || !strings.Contains(response.Body.String(), "[DONE]") {
		t.Fatalf("status=%d content_type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if runtime.streamRequest.RequestID == "" || runtime.streamRequest.Request.Model != "chat-model" || len(runtime.streamRequest.Request.Messages) != 2 || runtime.chatRequest.Request.Model != "" {
		t.Fatalf("stream request=%+v fallback request=%+v", runtime.streamRequest, runtime.chatRequest)
	}
	stream := response.Body.String()
	textEnd := strings.Index(stream, `"content":"1]"`)
	annotation := strings.Index(stream, `"source_citation"`)
	finish := strings.Index(stream, `"finish_reason":"stop"`)
	if textEnd < 0 || annotation < textEnd || finish < annotation || !strings.Contains(stream, `"source":"file_alpha"`) {
		t.Fatalf("stream citation order or source is invalid: %s", stream)
	}
	for _, line := range strings.Split(stream, "\n") {
		if !strings.HasPrefix(line, "data: {") || !strings.Contains(line, `"source_citation"`) {
			continue
		}
		var chunk struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Annotations []openai.ChatAnnotation `json:"annotations"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil || len(chunk.Choices) != 1 || chunk.Choices[0].Index != 0 || len(chunk.Choices[0].Delta.Annotations) != 1 {
			t.Fatalf("citation chunk=%+v err=%v", chunk, err)
		}
		citation := chunk.Choices[0].Delta.Annotations[0].SourceCitation
		if citation == nil || citation.Source != "file_alpha" || citation.StartIndex != 7 || citation.EndIndex != 10 {
			t.Fatalf("stream citation=%+v", citation)
		}
		return
	}
	t.Fatal("citation chunk is missing")
}

func TestRAGQueryStreamFallbackAndFailureCitationBehavior(t *testing.T) {
	for _, test := range []struct {
		name       string
		stream     bool
		streamFail bool
		citation   bool
	}{
		{name: "buffered fallback", citation: true},
		{name: "upstream failure", stream: true, streamFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &ragQueryProvider{stream: test.stream, streamFail: test.streamFail}
			_, handler := newRAGQueryHandler(t, "user", runtime)
			request := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader(`{"model":"chat-model","messages":[{"role":"user","content":"alpha"}],"stream":true,"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model","top_k":1}}`))
			request.Header.Set("Authorization", "Bearer key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || strings.Contains(response.Body.String(), `"source_citation"`) != test.citation {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if test.streamFail && !strings.Contains(response.Body.String(), `"provider_failed"`) {
				t.Fatalf("missing stream failure: %s", response.Body.String())
			}
		})
	}
}

func newRAGQueryHandler(t *testing.T, user string, runtime *ragQueryProvider, extraModules ...modules.Module) (*ragQueryAuthModule, http.Handler) {
	t.Helper()
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: user})
	vectors := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files: map[string]vectorstate.File{
			"vs_owned/file_alpha": {VectorStoreID: "vs_owned", FileID: "file_alpha", OwnerKey: owner, Status: "completed", Bytes: 10, CreatedAt: time.Unix(2, 0)},
			"vs_owned/file_beta":  {VectorStoreID: "vs_owned", FileID: "file_beta", OwnerKey: owner, Status: "completed", Bytes: 9, CreatedAt: time.Unix(1, 0)},
		},
	}
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_alpha": {ID: "file_alpha", OwnerKey: owner, Filename: "alpha.md", Purpose: "assistants", ContentType: "text/markdown", Bytes: 10, Content: []byte("alpha text")},
		"file_beta":  {ID: "file_beta", OwnerKey: owner, Filename: "beta.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 9, Content: []byte("beta text")},
	}}
	auth := &ragQueryAuthModule{user: user}
	pipelineModules := append([]modules.Module{auth}, extraModules...)
	handler := NewHandler(modules.NewPipeline(pipelineModules), runtime).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 4096})
	return auth, Routes(handler)
}

func TestRAGQueryRevalidatesRerankPolicyOutputAndEffectiveModel(t *testing.T) {
	for _, test := range []struct {
		name    string
		rewrite func(*openai.RerankRequest)
		status  int
		code    string
	}{
		{name: "invalid top n", rewrite: func(request *openai.RerankRequest) {
			zero := 0
			request.TopN = &zero
		}, status: http.StatusBadGateway, code: "module_failed"},
		{name: "unauthorized effective model", rewrite: func(request *openai.RerankRequest) {
			request.Model = "forbidden-model"
		}, status: http.StatusForbidden, code: "model_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &ragQueryProvider{}
			_, handler := newRAGQueryHandler(t, "user", runtime, rewriteContextModule{rewrite: func(req *modules.RequestContext) {
				if req.RerankRequest != nil {
					test.rewrite(req.RerankRequest)
				}
			}})
			request := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader(`{"model":"chat-model","messages":[{"role":"user","content":"alpha"}],"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model","top_k":2},"rerank":{"enabled":true,"model":"rerank-model","top_n":1}}`))
			request.Header.Set("Authorization", "Bearer key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || runtime.embeddingRequest.EmbeddingRequest == nil || runtime.rerankRequest.RerankRequest != nil || runtime.chatRequest.Request.Model != "" || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("invalid effective rerank continued: status=%d embedding=%+v rerank=%+v chat=%+v body=%s", response.Code, runtime.embeddingRequest, runtime.rerankRequest, runtime.chatRequest, response.Body.String())
			}
		})
	}
}

func TestRAGQueryRunsOwnerScopedRetrievalRerankAndChatWithDistinctExecutions(t *testing.T) {
	runtime := &ragQueryProvider{}
	auth, handler := newRAGQueryHandler(t, "user", runtime)
	body := `{"model":"chat-model","messages":[{"role":"system","content":"Be concise"},{"role":"user","content":"alpha"}],"temperature":0.2,"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model","top_k":2},"rerank":{"enabled":true,"model":"rerank-model","top_n":1}}`
	request := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer key")
	request.Header.Set("X-Request-ID", "shared-external-correlation")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || auth.calls != 2 || !strings.Contains(response.Body.String(), `"id":"chatcmpl-rag"`) {
		t.Fatalf("status=%d auth=%d body=%s", response.Code, auth.calls, response.Body.String())
	}
	var completion openai.ChatCompletionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &completion); err != nil || len(completion.Choices) != 1 || len(completion.Choices[0].Message.Annotations) != 1 {
		t.Fatalf("completion=%+v err=%v", completion, err)
	}
	citation := completion.Choices[0].Message.Annotations[0].SourceCitation
	if citation == nil || citation.Source != "file_beta" || citation.Title != "beta.txt" || citation.StartIndex != 7 || citation.EndIndex != 10 || citation.DocumentIndex == nil || *citation.DocumentIndex != 0 || citation.LocationType != "document_chunk" {
		t.Fatalf("citation=%+v", citation)
	}
	if runtime.embeddingRequest.EmbeddingRequest == nil || runtime.rerankRequest.RerankRequest == nil || runtime.chatRequest.Request.Model != "chat-model" {
		t.Fatalf("missing stage requests: embedding=%+v rerank=%+v chat=%+v", runtime.embeddingRequest, runtime.rerankRequest, runtime.chatRequest)
	}
	ids := map[string]bool{
		runtime.embeddingRequest.RequestID: true,
		runtime.rerankRequest.RequestID:    true,
		runtime.chatRequest.RequestID:      true,
	}
	if len(ids) != 3 || ids[""] || response.Header().Get("X-Execution-ID") != runtime.chatRequest.RequestID {
		t.Fatalf("execution IDs embedding=%q rerank=%q chat=%q header=%q", runtime.embeddingRequest.RequestID, runtime.rerankRequest.RequestID, runtime.chatRequest.RequestID, response.Header().Get("X-Execution-ID"))
	}
	if runtime.embeddingRequest.Metadata["gateway.api_type"] != "rag_query_retrieval" || runtime.rerankRequest.Metadata["gateway.api_type"] != "rag_query_rerank" || runtime.chatRequest.Metadata["gateway.api_type"] != "rag_query" {
		t.Fatalf("stage metadata embedding=%v rerank=%v chat=%v", runtime.embeddingRequest.Metadata, runtime.rerankRequest.Metadata, runtime.chatRequest.Metadata)
	}
	if len(runtime.chatRequest.Request.Messages) != 3 || runtime.chatRequest.Request.Messages[0].Role != "system" || runtime.chatRequest.Request.Messages[1].Role != "developer" || runtime.chatRequest.Request.Temperature == nil {
		t.Fatalf("chat request=%+v", runtime.chatRequest.Request)
	}
	contextText, ok := runtime.chatRequest.Request.Messages[1].Content.(string)
	if !ok || !strings.Contains(contextText, `"file_id":"file_beta"`) || !strings.Contains(contextText, "beta text") || strings.Contains(contextText, "alpha text") {
		t.Fatalf("retrieved context=%q", contextText)
	}
	if documents := runtime.rerankRequest.RerankRequest.Documents; len(documents) != 2 || documents[0] != "alpha text" || documents[1] != "beta text" {
		t.Fatalf("rerank documents=%v", documents)
	}
}

func TestRAGQueryRejectsInvalidChatBeforeRetrievalAndIsolatesOwners(t *testing.T) {
	for _, test := range []struct {
		name   string
		user   string
		body   string
		status int
	}{
		{name: "invalid chat", user: "user", body: `{"model":"chat-model","messages":[{"role":"user","content":"alpha"}],"max_tokens":1,"max_completion_tokens":1,"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model"}}`, status: http.StatusBadRequest},
		{name: "invalid rerank", user: "user", body: `{"model":"chat-model","messages":[{"role":"user","content":"alpha"}],"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model","top_k":1},"rerank":{"enabled":true,"model":"rerank-model","top_n":2}}`, status: http.StatusBadRequest},
		{name: "different owner", user: "other", body: `{"model":"chat-model","messages":[{"role":"user","content":"alpha"}],"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model"}}`, status: http.StatusNotFound},
		{name: "unknown field", user: "user", body: `{"model":"chat-model","messages":[{"role":"user","content":"alpha"}],"retrieval_config":{"vector_store_id":"vs_owned","model":"embed-model","unknown":true}}`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := &ragQueryProvider{}
			auth, handler := newRAGQueryHandler(t, "user", runtime)
			auth.user = test.user
			request := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer key")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || runtime.embeddingRequest.EmbeddingRequest != nil || runtime.chatRequest.Request.Model != "" {
				t.Fatalf("status=%d want=%d embedding=%+v chat=%+v body=%s", response.Code, test.status, runtime.embeddingRequest, runtime.chatRequest, response.Body.String())
			}
		})
	}
}

func TestDecodeRAGQueryPreservesChatParameters(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/rag/query", strings.NewReader(`{"model":"chat-model","messages":[{"role":"user","content":"hello"}],"reasoning_effort":"high","service_tier":"priority","retrieval_config":{"vector_store_id":"vs_owned"}}`))
	response := httptest.NewRecorder()
	chat, retrieval, _, ok := decodeRAGQueryRequest(response, request)
	if !ok || response.Code != http.StatusOK || chat.ReasoningEffort != "high" || chat.ServiceTier != "priority" || retrieval.VectorStoreID != "vs_owned" {
		payload, _ := json.Marshal(chat)
		t.Fatalf("ok=%t status=%d chat=%s retrieval=%+v body=%s", ok, response.Code, payload, retrieval, response.Body.String())
	}
}

func TestAnnotateRAGResponseUsesRuneOffsetsAndKnownSources(t *testing.T) {
	response := openai.ChatCompletionResponse{Choices: []openai.Choice{{Message: openai.Message{Role: "assistant", Content: "Ответ [2], см. [1](https://example.com), `[1]`, ```\n[1]\n``` и [9]."}}}}
	results := []vectorSearchResult{{FileID: "file_one", Filename: "one.txt", chunkIndex: 3}, {FileID: "file_two", Filename: "two.txt", chunkIndex: 7}}
	annotated := annotateRAGResponse(response, results)
	annotations := annotated.Choices[0].Message.Annotations
	if len(annotations) != 1 {
		t.Fatalf("annotations=%+v", annotations)
	}
	citation := annotations[0].SourceCitation
	if citation == nil || citation.Source != "file_two" || citation.StartIndex != 6 || citation.EndIndex != 9 || citation.LocationStart != 7 || citation.LocationEnd != 8 || citation.DocumentIndex == nil || *citation.DocumentIndex != 1 {
		t.Fatalf("citation=%+v", citation)
	}
}

func TestRAGStreamCitationsTrackChoicesIndependently(t *testing.T) {
	citations := newRAGStreamCitations([]vectorSearchResult{{FileID: "file_one", Filename: "one.txt"}, {FileID: "file_two", Filename: "two.txt"}})
	first := `{"id":"chat","object":"chat.completion.chunk","model":"model","choices":[{"index":0,"delta":{"content":"A ["},"finish_reason":null},{"index":1,"delta":{"content":"B ["},"finish_reason":null}]}`
	if output, err := citations.decorate(first); err != nil || len(output) != 1 || output[0] != first {
		t.Fatalf("first output=%v err=%v", output, err)
	}
	last := `{"id":"chat","object":"chat.completion.chunk","model":"model","choices":[{"index":0,"delta":{"content":"1]"},"finish_reason":"stop"},{"index":1,"delta":{"content":"2]"},"finish_reason":"stop"}]}`
	output, err := citations.decorate(last)
	if err != nil || len(output) != 4 || !strings.Contains(output[0], `"content":"1]"`) || !strings.Contains(output[3], `"finish_reason":"stop"`) {
		t.Fatalf("last output=%v err=%v", output, err)
	}
	for index, raw := range output[1:3] {
		var chunk struct {
			Choices []struct {
				Index int `json:"index"`
				Delta struct {
					Annotations []openai.ChatAnnotation `json:"annotations"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(raw), &chunk); err != nil || len(chunk.Choices) != 1 || chunk.Choices[0].Index != index || len(chunk.Choices[0].Delta.Annotations) != 1 || chunk.Choices[0].Delta.Annotations[0].SourceCitation == nil || chunk.Choices[0].Delta.Annotations[0].SourceCitation.Source != []string{"file_one", "file_two"}[index] {
			t.Fatalf("index=%d chunk=%+v err=%v", index, chunk, err)
		}
	}
}
