package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/filestate"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"ai-gateway-gateway/internal/vectorstate"
)

type vectorSearchAuthModule struct{ calls int }

func (*vectorSearchAuthModule) Name() string   { return "auth" }
func (*vectorSearchAuthModule) Required() bool { return true }
func (m *vectorSearchAuthModule) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	req.CredentialID = "credential"
	req.UserID = "user"
	req.AllowedModels = []string{"embed-model"}
	req.RateLimitTPM = 1000
	return nil
}

type vectorSearchPolicyRecorder struct {
	calls  int
	inputs int
}

func (*vectorSearchPolicyRecorder) Name() string   { return "dlp" }
func (*vectorSearchPolicyRecorder) Required() bool { return true }
func (m *vectorSearchPolicyRecorder) Handle(_ context.Context, req *modules.RequestContext) error {
	m.calls++
	if values, ok := req.EmbeddingRequest.Input.([]string); ok {
		m.inputs = len(values)
	}
	return nil
}

type vectorSearchProvider struct {
	modelsProvider
	request modules.RequestContext
}

func (p *vectorSearchProvider) Embeddings(_ context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error) {
	p.request = req
	inputs := req.EmbeddingRequest.Input.([]string)
	data := make([]openai.Embedding, len(inputs))
	for index, input := range inputs {
		vector := []float64{0, 1}
		if input == "alpha" || strings.Contains(input, "alpha") {
			vector = []float64{1, 0}
		}
		data[index] = openai.Embedding{Object: "embedding", Embedding: vector, Index: index}
	}
	return openai.EmbeddingResponse{UsageReported: true, Object: "list", Model: "embed-model", Data: data, Usage: openai.Usage{PromptTokens: len(inputs), TotalTokens: len(inputs)}}, nil
}

func TestVectorStoreSearchUsesOwnedTextPolicyAndEmbeddingBilling(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	vectors := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files: map[string]vectorstate.File{
			"vs_owned/file_alpha": {VectorStoreID: "vs_owned", FileID: "file_alpha", OwnerKey: owner, Status: "completed", Bytes: 10, CreatedAt: time.Unix(2, 0)},
			"vs_owned/file_beta":  {VectorStoreID: "vs_owned", FileID: "file_beta", OwnerKey: owner, Status: "completed", Bytes: 9, CreatedAt: time.Unix(1, 0)},
		},
	}
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_alpha": {ID: "file_alpha", OwnerKey: owner, Filename: "alpha.md", Purpose: "assistants", ContentType: "text/markdown", Bytes: 10, Content: []byte("alpha text")},
		"file_beta":  {ID: "file_beta", OwnerKey: owner, Filename: "beta.txt", Purpose: "assistants", ContentType: "text/plain; charset=utf-8", Bytes: 9, Content: []byte("beta text")},
	}}
	auth := &vectorSearchAuthModule{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openai.EmbeddingRequest
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer provider-key" || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Fatalf("invalid upstream request: path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		inputs := request.Input.([]any)
		if request.Model != "embed-upstream" || len(inputs) != 3 || inputs[0] != "alpha" {
			t.Fatalf("upstream request=%+v", request)
		}
		_, _ = fmt.Fprint(w, `{"object":"list","model":"embed-upstream","data":[{"object":"embedding","index":0,"embedding":[1,0]},{"object":"embedding","index":1,"embedding":[0,1]},{"object":"embedding","index":2,"embedding":[1,0]}],"usage":{"prompt_tokens":7,"total_tokens":7}}`)
	}))
	defer upstream.Close()
	usage := &embeddingUsageRecorder{}
	policy := &vectorSearchPolicyRecorder{}
	requestPolicy := &vectorSearchPolicyRecorder{}
	embedder := provider.New(provider.Config{Endpoints: []config.ProviderEndpointConfig{{Name: "embed", Type: "openai-compatible", BaseURL: upstream.URL, APIKey: "provider-key", Models: []string{"embed-model"}, ModelAliases: map[string]string{"embed-model": "embed-upstream"}, Capabilities: []string{"embeddings"}}}, Modules: modules.NewPipeline([]modules.Module{policy, usage})})
	rates := &embeddingTokenRateStore{}
	handler := NewHandlerWithRateLimitStore(modules.NewPipeline([]modules.Module{auth, requestPolicy}), embedder, rates).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 4096})

	request := httptest.NewRequest(http.MethodPost, "/v1/vector_stores/vs_owned/search", strings.NewReader(`{"query":"alpha","model":"embed-model","max_num_results":1}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || auth.calls != 1 || requestPolicy.calls != 1 || requestPolicy.inputs != 3 || policy.calls != 1 || policy.inputs != 3 || rates.tokens <= openai.EmbeddingInputTokenCount("alpha") || usage.pre != 1 || usage.post != 1 || usage.usage.TotalTokens != 7 {
		t.Fatalf("status=%d auth=%d request_policy=%+v provider_policy=%+v rate_tokens=%d billing=%+v body=%s", response.Code, auth.calls, requestPolicy, policy, rates.tokens, usage, response.Body.String())
	}
	var body struct {
		Object      string               `json:"object"`
		SearchQuery []string             `json:"search_query"`
		Data        []vectorSearchResult `json:"data"`
		HasMore     bool                 `json:"has_more"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Object != "vector_store.search_results.page" || len(body.SearchQuery) != 1 || body.SearchQuery[0] != "alpha" || body.HasMore || len(body.Data) != 1 || body.Data[0].FileID != "file_alpha" || body.Data[0].Content[0].Text != "alpha text" {
		t.Fatalf("response=%+v", body)
	}
}

func TestVectorStoreSearchRejectsMissingUnsupportedAndOversizedStores(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	vectors := &memoryVectorStore{stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner}}, files: map[string]vectorstate.File{}}
	files := &memoryFileStore{files: map[string]filestate.File{}}
	auth := &vectorSearchAuthModule{}
	embedder := &vectorSearchProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{auth}), embedder).WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 100, ByteQuota: 4096})
	call := func(path, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer key")
		response := httptest.NewRecorder()
		Routes(handler).ServeHTTP(response, request)
		return response
	}
	if response := call("/v1/vector_stores/missing/search", `{"query":"x","model":"embed-model"}`); response.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", response.Code, response.Body.String())
	}
	vectors.files["vs_owned/file_binary"] = vectorstate.File{VectorStoreID: "vs_owned", FileID: "file_binary", OwnerKey: owner, Status: "completed", Bytes: 4}
	files.files["file_binary"] = filestate.File{ID: "file_binary", OwnerKey: owner, Filename: "data.bin", ContentType: "application/octet-stream", Bytes: 4, Content: []byte{1, 2, 3, 4}}
	if response := call("/v1/vector_stores/vs_owned/search", `{"query":"x","model":"embed-model"}`); response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("binary status=%d body=%s", response.Code, response.Body.String())
	}
	delete(vectors.files, "vs_owned/file_binary")
	delete(files.files, "file_binary")
	for index := 0; index <= maxVectorSearchFiles; index++ {
		id := "file_" + string(rune('a'+index))
		vectors.files["vs_owned/"+id] = vectorstate.File{VectorStoreID: "vs_owned", FileID: id, OwnerKey: owner, Status: "completed", Bytes: 1, CreatedAt: time.Unix(int64(index), 0)}
		files.files[id] = filestate.File{ID: id, OwnerKey: owner, Filename: id + ".txt", ContentType: "text/plain", Bytes: 1, Content: []byte("x")}
	}
	if response := call("/v1/vector_stores/vs_owned/search", `{"query":"x","model":"embed-model"}`); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestVectorSearchRankingRejectsMalformedVectors(t *testing.T) {
	chunks := []vectorSearchChunk{{fileID: "file", filename: "file.txt", text: "text"}}
	valid := openai.EmbeddingResponse{Data: []openai.Embedding{{Index: 0, Embedding: []float64{1, 0}}, {Index: 1, Embedding: []float64{1, 1}}}}
	if results, ok := rankVectorSearchResults(valid, chunks, 1); !ok || len(results) != 1 || results[0].Score <= 0 {
		t.Fatalf("valid results=%+v ok=%t", results, ok)
	}
	for _, response := range []openai.EmbeddingResponse{
		{Data: valid.Data[:1]},
		{Data: []openai.Embedding{{Index: 0, Embedding: []float64{1}}, {Index: 1, Embedding: []float64{1, 1}}}},
		{Data: []openai.Embedding{{Index: 0, Embedding: []float64{0, 0}}, {Index: 1, Embedding: []float64{1, 1}}}},
	} {
		if _, ok := rankVectorSearchResults(response, chunks, 1); ok {
			t.Fatalf("accepted malformed response=%+v", response)
		}
	}
}
