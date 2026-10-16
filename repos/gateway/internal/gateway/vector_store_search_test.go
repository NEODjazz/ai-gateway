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
	"unicode/utf8"

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
	calls   int
}

func (p *vectorSearchProvider) Embeddings(_ context.Context, req modules.RequestContext) (openai.EmbeddingResponse, error) {
	p.calls++
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

func TestVectorStoreSearchRevalidatesPolicyOutputAndEffectiveModel(t *testing.T) {
	for _, test := range []struct {
		name    string
		rewrite func(*modules.RequestContext)
		status  int
		code    string
	}{
		{name: "invalid dimensions", rewrite: func(req *modules.RequestContext) {
			dimensions := 0
			req.EmbeddingRequest.Dimensions = &dimensions
		}, status: http.StatusBadGateway, code: "module_failed"},
		{name: "unauthorized effective model", rewrite: func(req *modules.RequestContext) {
			req.EmbeddingRequest.Model = "forbidden-model"
		}, status: http.StatusForbidden, code: "model_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
			vectors := &memoryVectorStore{
				stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner}},
				files:  map[string]vectorstate.File{"vs_owned/file_text": {VectorStoreID: "vs_owned", FileID: "file_text", OwnerKey: owner, Status: "completed", Bytes: 4}},
			}
			files := &memoryFileStore{files: map[string]filestate.File{
				"file_text": {ID: "file_text", OwnerKey: owner, Filename: "text.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 4, Content: []byte("text")},
			}}
			embedder := &vectorSearchProvider{}
			handler := NewHandler(modules.NewPipeline([]modules.Module{&vectorSearchAuthModule{}, rewriteContextModule{rewrite: test.rewrite}}), embedder).
				WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
				WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 4096})
			request := httptest.NewRequest(http.MethodPost, "/v1/vector_stores/vs_owned/search", strings.NewReader(`{"query":"query","model":"embed-model"}`))
			request.Header.Set("Authorization", "Bearer key")
			response := httptest.NewRecorder()
			Routes(handler).ServeHTTP(response, request)
			if response.Code != test.status || embedder.calls != 0 || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
				t.Fatalf("invalid effective request continued: status=%d calls=%d body=%s", response.Code, embedder.calls, response.Body.String())
			}
		})
	}
}

func TestVectorStoreSearchAnonymizesStringSliceEmbeddingInput(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	vectors := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner}},
		files:  map[string]vectorstate.File{"vs_owned/file_text": {VectorStoreID: "vs_owned", FileID: "file_text", OwnerKey: owner, Status: "completed", Bytes: 17}},
	}
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_text": {ID: "file_text", OwnerKey: owner, Filename: "text.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: 17, Content: []byte("owner@example.com")},
	}}
	embedder := &vectorSearchProvider{}
	handler := NewHandler(modules.NewPipeline([]modules.Module{&vectorSearchAuthModule{}, modules.NewAnonymizerModule(true, modules.RuleEmail)}), embedder).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 1024, OwnerQuotaBytes: 4096}).
		WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 4096})
	request := httptest.NewRequest(http.MethodPost, "/v1/vector_stores/vs_owned/search", strings.NewReader(`{"query":"user@example.com","model":"embed-model"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || embedder.calls != 1 || embedder.request.EmbeddingRequest == nil {
		t.Fatalf("status=%d calls=%d request=%+v body=%s", response.Code, embedder.calls, embedder.request, response.Body.String())
	}
	inputs, ok := embedder.request.EmbeddingRequest.Input.([]string)
	if !ok || len(inputs) != 2 || inputs[0] != "{{EMAIL_1}}" || inputs[1] != "{{EMAIL_2}}" {
		t.Fatalf("embedding input was not anonymized: %#v", embedder.request.EmbeddingRequest.Input)
	}
}

func TestVectorStoreSearchUsesOwnedTextPolicyAndEmbeddingBilling(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	vectors := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner, Name: "docs", Status: "completed"}},
		files: map[string]vectorstate.File{
			"vs_owned/file_alpha": {VectorStoreID: "vs_owned", FileID: "file_alpha", OwnerKey: owner, Status: "completed", Bytes: 10, Attributes: map[string]any{"region": "eu", "priority": float64(2), "active": true}, CreatedAt: time.Unix(2, 0)},
			"vs_owned/file_beta":  {VectorStoreID: "vs_owned", FileID: "file_beta", OwnerKey: owner, Status: "completed", Bytes: 9, Attributes: map[string]any{"region": "us"}, CreatedAt: time.Unix(1, 0)},
		},
	}
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_alpha": {ID: "file_alpha", OwnerKey: owner, Filename: "alpha.md", Purpose: "assistants", ContentType: "text/markdown", Bytes: 10, Content: []byte("alpha text")},
	}}
	auth := &vectorSearchAuthModule{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request openai.EmbeddingRequest
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer provider-key" || json.NewDecoder(r.Body).Decode(&request) != nil {
			t.Fatalf("invalid upstream request: path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		inputs := request.Input.([]any)
		if request.Model != "embed-upstream" || len(inputs) != 2 || inputs[0] != "alpha" || inputs[1] != "alpha text" {
			t.Fatalf("upstream request=%+v", request)
		}
		_, _ = fmt.Fprint(w, `{"object":"list","model":"embed-upstream","data":[{"object":"embedding","index":0,"embedding":[1,0]},{"object":"embedding","index":1,"embedding":[1,0]}],"usage":{"prompt_tokens":5,"total_tokens":5}}`)
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

	request := httptest.NewRequest(http.MethodPost, "/v1/vector_stores/vs_owned/search", strings.NewReader(`{"query":"alpha","model":"embed-model","max_num_results":1,"filters":{"region":"eu"}}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	Routes(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK || auth.calls != 1 || requestPolicy.calls != 1 || requestPolicy.inputs != 2 || policy.calls != 1 || policy.inputs != 2 || rates.tokens <= openai.EmbeddingInputTokenCount("alpha") || usage.pre != 1 || usage.post != 1 || usage.usage.TotalTokens != 5 {
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
	if body.Object != "vector_store.search_results.page" || len(body.SearchQuery) != 1 || body.SearchQuery[0] != "alpha" || body.HasMore || len(body.Data) != 1 || body.Data[0].FileID != "file_alpha" || body.Data[0].Attributes["region"] != "eu" || body.Data[0].Attributes["priority"] != float64(2) || body.Data[0].Attributes["active"] != true || body.Data[0].Content[0].Text != "alpha text" {
		t.Fatalf("response=%+v", body)
	}
}

func TestVectorStoreSearchUsesPersistedStaticChunking(t *testing.T) {
	owner := fileOwnerKey(modules.RequestContext{CredentialID: "credential", UserID: "user"})
	content := strings.Repeat("alpha ", 180) + "данные"
	vectors := &memoryVectorStore{
		stores: map[string]vectorstate.VectorStore{"vs_owned": {ID: "vs_owned", OwnerKey: owner}},
		files: map[string]vectorstate.File{"vs_owned/file_text": {
			VectorStoreID: "vs_owned", FileID: "file_text", OwnerKey: owner, Status: "completed", Bytes: int64(len(content)),
			ChunkingStrategy: vectorstate.ChunkingStrategy{Type: "static", MaxChunkSizeTokens: 100, ChunkOverlapTokens: 25},
		}},
	}
	files := &memoryFileStore{files: map[string]filestate.File{
		"file_text": {ID: "file_text", OwnerKey: owner, Filename: "text.txt", Purpose: "assistants", ContentType: "text/plain", Bytes: int64(len(content)), Content: []byte(content)},
	}}
	embedder := &vectorSearchProvider{}
	handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{&vectorSearchAuthModule{}}), embedder).
		WithFileStore(files, FileRuntimeConfig{MaxBytes: 4096, OwnerQuotaBytes: 8192}).
		WithVectorStore(vectors, VectorStoreRuntimeConfig{OwnerQuota: 10, FileQuota: 10, ByteQuota: 8192}))
	request := httptest.NewRequest(http.MethodPost, "/v1/vector_stores/vs_owned/search", strings.NewReader(`{"query":"alpha","model":"embed-model"}`))
	request.Header.Set("Authorization", "Bearer key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || embedder.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, embedder.calls, response.Body.String())
	}
	inputs, ok := embedder.request.EmbeddingRequest.Input.([]string)
	if !ok || len(inputs) < 3 {
		t.Fatalf("static strategy did not split embedding input: %#v", embedder.request.EmbeddingRequest.Input)
	}
	for index, chunk := range inputs[1:] {
		if tokens := openai.EstimateContextTokens(chunk); tokens > 100 {
			t.Fatalf("chunk %d has %d estimated tokens: %q", index, tokens, chunk)
		}
	}
	if !strings.Contains(inputs[1], "alpha") || !strings.Contains(inputs[2], "alpha") {
		t.Fatalf("expected bounded overlap between chunks: %#v", inputs[1:3])
	}
}

func TestSplitVectorSearchTextStaticIsBoundedAndUnicodeSafe(t *testing.T) {
	strategy := vectorstate.ChunkingStrategy{Type: "static", MaxChunkSizeTokens: 100, ChunkOverlapTokens: 20}
	chunks := splitVectorSearchTextWithStrategy(strings.Repeat("данные alpha ", 220), strategy)
	if len(chunks) < 2 || len(chunks) > maxVectorSearchChunks {
		t.Fatalf("chunks=%d", len(chunks))
	}
	for index, chunk := range chunks {
		if !utf8.ValidString(chunk) {
			t.Fatalf("chunk %d is not valid UTF-8", index)
		}
		if tokens := openai.EstimateContextTokens(chunk); tokens > strategy.MaxChunkSizeTokens {
			t.Fatalf("chunk %d has %d estimated tokens", index, tokens)
		}
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

func TestVectorSearchFiltersSupportExactComparisonAndCompounds(t *testing.T) {
	attributes := map[string]any{"region": "eu", "category": "docs"}
	for _, test := range []struct {
		raw   string
		match bool
	}{
		{`{"region":"eu"}`, true},
		{`{"type":"document","region":"eu"}`, false},
		{`{"region":"EU"}`, false},
		{`{"type":"eq","key":"category","value":"docs"}`, true},
		{`{"type":"ne","key":"region","value":"us"}`, true},
		{`{"type":"in","key":"region","value":["us","eu"]}`, true},
		{`{"type":"nin","key":"region","value":["eu","apac"]}`, false},
		{`{"type":"and","filters":[{"type":"eq","key":"region","value":"eu"},{"type":"or","filters":[{"type":"eq","key":"category","value":"docs"},{"type":"eq","key":"category","value":"other"}]}]}`, true},
	} {
		filter, err := parseVectorSearchFilter(json.RawMessage(test.raw))
		if err != nil {
			t.Fatalf("filter=%s err=%v", test.raw, err)
		}
		if match := filter.matches(attributes); match != test.match {
			t.Fatalf("filter=%s match=%t want=%t", test.raw, match, test.match)
		}
	}
	numeric := map[string]any{"score": float64(10.5), "active": true}
	for _, raw := range []string{
		`{"type":"gt","key":"score","value":10}`,
		`{"type":"lte","key":"score","value":10.5}`,
		`{"type":"eq","key":"active","value":true}`,
	} {
		filter, err := parseVectorSearchFilter(json.RawMessage(raw))
		if err != nil || !filter.matches(numeric) {
			t.Fatalf("filter=%s did not match: %v", raw, err)
		}
	}
}

func TestVectorSearchFiltersRejectUnboundedOrMalformedTrees(t *testing.T) {
	invalid := []string{
		`null`,
		`{"type":"unknown","key":"x","value":"y"}`,
		`{"type":"gt","key":"x","value":"not-a-number"}`,
		`{"type":"in","key":"x","value":[]}`,
		`{"type":"and","filters":[]}`,
		`{"type":"eq","key":"x","value":"y","unknown":true}`,
		`{"type":"and","filters":[{"type":"and","filters":[{"type":"and","filters":[{"type":"and","filters":[{"type":"eq","key":"x","value":"y"}]}]}]}]}`,
	}
	for _, raw := range invalid {
		if _, err := parseVectorSearchFilter(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted invalid filter=%s", raw)
		}
	}
}
