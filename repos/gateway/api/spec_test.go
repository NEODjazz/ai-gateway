package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"ai-gateway-gateway/api"
	"ai-gateway-gateway/internal/gateway"
	"github.com/getkin/kin-openapi/openapi3"
)

func loadDocument(t *testing.T) *openapi3.T {
	t.Helper()
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromData(api.OpenAPI)
	if err != nil {
		t.Fatalf("load OpenAPI document: %v", err)
	}
	if err := document.Validate(context.Background()); err != nil {
		t.Fatalf("validate OpenAPI document: %v", err)
	}
	if document.OpenAPI != "3.1.0" {
		t.Fatalf("expected OpenAPI 3.1.0, got %q", document.OpenAPI)
	}
	return document
}

func TestOpenAPIDocumentIsValid(t *testing.T) {
	loadDocument(t)
}

func TestOpenAPIProviderProfilesExposeModelSpecificChatPolicy(t *testing.T) {
	document := loadDocument(t)
	profile := document.Components.Schemas["ProviderCapabilityProfile"].Value
	if profile == nil || profile.Properties["chat_model_parameters"] == nil {
		t.Fatal("ProviderCapabilityProfile is missing chat_model_parameters")
	}
	policy := document.Components.Schemas["ProviderChatModelParameterPolicy"].Value
	if policy == nil || policy.Properties["model"] == nil || policy.Properties["reasoning_effort"] == nil {
		t.Fatal("ProviderChatModelParameterPolicy is incomplete")
	}
	models := policy.Properties["model"].Value.Enum
	if len(models) != 3 || models[0] != "openai/gpt-oss-20b" || models[1] != "openai/gpt-oss-120b" || models[2] != "deepseek-ai/DeepSeek-V4-Pro-0813" {
		t.Fatalf("model-specific chat policy models=%v", models)
	}
}

func TestOpenAPITopKBelongsToMessagesRequest(t *testing.T) {
	document := loadDocument(t)
	messages := document.Components.Schemas["MessagesRequest"].Value
	if messages == nil || messages.Properties["top_k"] == nil {
		t.Fatal("MessagesRequest is missing top_k")
	}
	run := document.Components.Schemas["AssistantRunCreateRequest"].Value
	if run != nil && run.Properties["top_k"] != nil {
		t.Fatal("AssistantRunCreateRequest unexpectedly exposes top_k")
	}
}

func TestOpenAPIZeroOutputBelongsToMessagesRequest(t *testing.T) {
	document := loadDocument(t)
	messages := document.Components.Schemas["MessagesRequest"].Value
	if messages == nil || messages.Properties["max_tokens"] == nil || messages.Properties["max_tokens"].Value == nil {
		t.Fatal("MessagesRequest is missing max_tokens")
	}
	minimum := messages.Properties["max_tokens"].Value.Min
	if minimum == nil || *minimum != 0 {
		t.Fatalf("MessagesRequest max_tokens minimum = %v, want 0", minimum)
	}
	chat := document.Components.Schemas["ChatCompletionRequest"].Value
	if chat == nil || chat.Properties["max_tokens"] == nil || chat.Properties["max_tokens"].Value == nil {
		t.Fatal("ChatCompletionRequest is missing max_tokens")
	}
	chatMinimum := chat.Properties["max_tokens"].Value.Min
	if chatMinimum == nil || *chatMinimum != 1 {
		t.Fatalf("ChatCompletionRequest max_tokens minimum = %v, want 1", chatMinimum)
	}
}

func TestOpenAPIMessagesDoesNotAdvertiseUnsupportedLifecycleFields(t *testing.T) {
	document := loadDocument(t)
	messages := document.Components.Schemas["MessagesRequest"].Value
	if messages == nil {
		t.Fatal("MessagesRequest is missing")
	}
	for _, field := range []string{"background", "stream_options"} {
		if messages.Properties[field] != nil {
			t.Errorf("MessagesRequest unexpectedly advertises unsupported field %q", field)
		}
	}
}

func TestOpenAPIMessagesAdvertisesTopLevelCacheControl(t *testing.T) {
	document := loadDocument(t)
	messages := document.Components.Schemas["MessagesRequest"].Value
	control := messages.Properties["cache_control"]
	if control == nil || control.Value == nil {
		t.Fatal("MessagesRequest is missing cache_control")
	}
	if control.Value.Properties["type"] == nil || control.Value.Properties["ttl"] == nil {
		t.Fatal("MessagesRequest cache_control is incomplete")
	}
}

func TestOpenAPIMessagesAdvertisesInferenceGeography(t *testing.T) {
	document := loadDocument(t)
	messages := document.Components.Schemas["MessagesRequest"].Value
	geo := messages.Properties["inference_geo"]
	if geo == nil || geo.Value == nil || len(geo.Value.Enum) != 2 || geo.Value.Enum[0] != "global" || geo.Value.Enum[1] != "us" {
		t.Fatalf("MessagesRequest inference_geo enum = %#v", geo)
	}
	count := document.Components.Schemas["MessageTokenCountRequest"].Value
	if count == nil || count.Properties["inference_geo"] == nil || count.Properties["cache_control"] == nil {
		t.Fatal("MessageTokenCountRequest does not preserve inference_geo and cache_control")
	}
	response := document.Components.Schemas["MessagesResponse"].Value
	usage := response.Properties["usage"].Value
	if usage == nil || usage.Properties["inference_geo"] == nil {
		t.Fatal("MessagesResponse usage is missing inference_geo")
	}
}

func TestOpenAPIMessagesAdvertisesContextManagement(t *testing.T) {
	document := loadDocument(t)
	messages := document.Components.Schemas["MessagesRequest"].Value
	if messages == nil || messages.Properties["context_management"] == nil {
		t.Fatal("MessagesRequest is missing context_management")
	}
	count := document.Components.Schemas["MessageTokenCountRequest"].Value
	if count == nil || count.Properties["context_management"] == nil {
		t.Fatal("MessageTokenCountRequest is missing context_management")
	}
	response := document.Components.Schemas["MessagesResponse"].Value
	if response == nil || response.Properties["context_management"] == nil {
		t.Fatal("MessagesResponse is missing context_management")
	}
	context := document.Components.Schemas["MessagesContextManagement"].Value
	if context == nil || context.Properties["edits"] == nil || context.Properties["edits"].Value == nil || context.Properties["edits"].Value.MinItems != 1 || context.Properties["edits"].Value.MaxItems == nil || *context.Properties["edits"].Value.MaxItems != 2 {
		t.Fatalf("MessagesContextManagement edits bounds are incomplete: %#v", context)
	}
}

func TestOpenAPIMessagesAdvertisesFailedToolResults(t *testing.T) {
	document := loadDocument(t)
	block := document.Components.Schemas["MessagesToolResultBlock"].Value
	if block == nil || block.Properties["is_error"] == nil || block.Properties["content"] == nil {
		t.Fatalf("MessagesToolResultBlock is incomplete: %#v", block)
	}
	if block.Properties["is_error"].Value == nil || block.Properties["is_error"].Value.Type == nil || !block.Properties["is_error"].Value.Type.Is("boolean") {
		t.Fatalf("MessagesToolResultBlock.is_error is not boolean: %#v", block.Properties["is_error"])
	}
}

func TestOpenAPIMessagesAdvertisesBoundedDocuments(t *testing.T) {
	document := loadDocument(t)
	block := document.Components.Schemas["MessagesDocumentBlock"].Value
	if block == nil || block.Properties["source"] == nil || block.Properties["source"].Value == nil || block.Properties["citations"] == nil || block.Properties["citations"].Value == nil || block.Properties["title"] == nil || block.Properties["context"] == nil {
		t.Fatalf("MessagesDocumentBlock is incomplete: %#v", block)
	}
	source := block.Properties["source"].Value
	if len(source.OneOf) != 4 || source.OneOf[0].Value == nil || source.OneOf[1].Value == nil || source.OneOf[2].Value == nil || source.OneOf[3].Value == nil {
		t.Fatalf("MessagesDocumentBlock source is incomplete: %#v", source)
	}
	pdf, text := source.OneOf[0].Value, source.OneOf[1].Value
	if pdf.Properties["data"] == nil || pdf.Properties["media_type"] == nil || pdf.Properties["media_type"].Value == nil || pdf.Properties["media_type"].Value.Const != "application/pdf" {
		t.Fatalf("MessagesDocumentBlock PDF source is incomplete: %#v", pdf)
	}
	textData := text.Properties["data"].Value
	if text.Properties["media_type"] == nil || text.Properties["media_type"].Value == nil || text.Properties["media_type"].Value.Const != "text/plain" || textData == nil || textData.MinLength != 1 || textData.MaxLength == nil || *textData.MaxLength != 262144 {
		t.Fatalf("MessagesDocumentBlock text source is incomplete: %#v", text)
	}
	file := source.OneOf[2].Value
	if file.Properties["type"] == nil || file.Properties["type"].Value == nil || file.Properties["type"].Value.Const != "file" || file.Properties["file_id"] == nil || file.Properties["file_id"].Value == nil || file.Properties["file_id"].Value.MaxLength == nil || *file.Properties["file_id"].Value.MaxLength != 128 {
		t.Fatalf("MessagesDocumentBlock file source is incomplete: %#v", file)
	}
	remote := source.OneOf[3].Value
	if remote.Properties["type"] == nil || remote.Properties["type"].Value == nil || remote.Properties["type"].Value.Const != "url" || remote.Properties["url"] == nil || remote.Properties["url"].Value == nil || remote.Properties["url"].Value.Pattern != "^https://" || remote.Properties["url"].Value.MaxLength == nil || *remote.Properties["url"].Value.MaxLength != 2048 {
		t.Fatalf("MessagesDocumentBlock URL source is incomplete: %#v", remote)
	}
	citations := block.Properties["citations"].Value
	if citations.Properties["enabled"] == nil || citations.Properties["enabled"].Value == nil || citations.Properties["enabled"].Value.Const != true {
		t.Fatalf("MessagesDocumentBlock citations are incomplete: %#v", citations)
	}
	title, context := block.Properties["title"].Value, block.Properties["context"].Value
	if title == nil || title.MinLength != 1 || title.MaxLength == nil || *title.MaxLength != 512 || context == nil || context.MinLength != 1 || context.MaxLength == nil || *context.MaxLength != 8192 {
		t.Fatalf("MessagesDocumentBlock metadata bounds are incomplete: title=%#v context=%#v", title, context)
	}
}

func TestOpenAPIMessagesAdvertisesBoundedURLImages(t *testing.T) {
	document := loadDocument(t)
	block := document.Components.Schemas["MessagesImageBlock"].Value
	if block == nil || block.Properties["source"] == nil || block.Properties["source"].Value == nil {
		t.Fatalf("MessagesImageBlock is incomplete: %#v", block)
	}
	source := block.Properties["source"].Value
	if len(source.OneOf) != 3 || source.OneOf[0].Value == nil || source.OneOf[1].Value == nil || source.OneOf[2].Value == nil {
		t.Fatalf("MessagesImageBlock source is incomplete: %#v", source)
	}
	remote := source.OneOf[1].Value
	if remote.Properties["type"] == nil || remote.Properties["type"].Value == nil || remote.Properties["type"].Value.Const != "url" || remote.Properties["url"] == nil || remote.Properties["url"].Value == nil || remote.Properties["url"].Value.Pattern != "^https://" || remote.Properties["url"].Value.MaxLength == nil || *remote.Properties["url"].Value.MaxLength != 2048 {
		t.Fatalf("MessagesImageBlock URL source is incomplete: %#v", remote)
	}
	file := source.OneOf[2].Value
	if file.Properties["type"] == nil || file.Properties["type"].Value == nil || file.Properties["type"].Value.Const != "file" || file.Properties["file_id"] == nil || file.Properties["file_id"].Value == nil || file.Properties["file_id"].Value.MaxLength == nil || *file.Properties["file_id"].Value.MaxLength != 128 {
		t.Fatalf("MessagesImageBlock file source is incomplete: %#v", file)
	}
}

func TestOpenAPIRoutesMatchGatewayRouter(t *testing.T) {
	document := loadDocument(t)
	want := map[string]bool{}
	for path, item := range document.Paths.Map() {
		for method := range item.Operations() {
			want[method+" "+path] = true
		}
	}
	got := map[string]bool{}
	for _, route := range gateway.DocumentedRoutes() {
		key := route.Method + " " + route.Path
		got[key] = true
		item := document.Paths.Find(route.Path)
		if item == nil || item.GetOperation(route.Method) == nil {
			t.Errorf("registered route %s is absent from OpenAPI", key)
		}
	}
	for key := range want {
		if !got[key] {
			t.Errorf("OpenAPI operation %s is not registered by gateway", key)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("route count mismatch: router=%d OpenAPI=%d", len(got), len(want))
	}
}

func TestOpenAPIOperationIDsAreUnique(t *testing.T) {
	document := loadDocument(t)
	seen := map[string]string{}
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			if operation.OperationID == "" {
				t.Errorf("%s %s has no operationId", method, path)
				continue
			}
			if previous := seen[operation.OperationID]; previous != "" {
				t.Errorf("operationId %q is shared by %s and %s %s", operation.OperationID, previous, method, path)
			}
			seen[operation.OperationID] = method + " " + path
		}
	}
}

func TestOpenAPIRequestExamplesMatchSchemas(t *testing.T) {
	document := loadDocument(t)
	for path, item := range document.Paths.Map() {
		for method, operation := range item.Operations() {
			if operation.RequestBody == nil || operation.RequestBody.Value == nil {
				continue
			}
			media := operation.RequestBody.Value.Content.Get("application/json")
			if media == nil || media.Schema == nil || media.Schema.Value == nil {
				continue
			}
			if media.Example != nil {
				if err := media.Schema.Value.VisitJSON(media.Example, openapi3.EnableJSONSchema2020()); err != nil {
					t.Errorf("%s %s request example: %v", method, path, err)
				}
			}
			for name, example := range media.Examples {
				if example == nil || example.Value == nil {
					continue
				}
				if err := media.Schema.Value.VisitJSON(example.Value.Value, openapi3.EnableJSONSchema2020()); err != nil {
					t.Errorf("%s %s example %q: %v", method, path, name, err)
				}
			}
		}
	}
}

func TestOpenAPIDoesNotExposeInternalServiceContracts(t *testing.T) {
	specification := string(api.OpenAPI)
	for _, path := range []string{"/authorize", "/usage", "/scan", "/anonymize", "/internal/v1/keys", "/internal/v1/budgets"} {
		if strings.Contains(specification, "\n  "+path+":") {
			t.Errorf("OpenAPI exposes internal route %q", path)
		}
	}
}

func TestDocumentedRouteMethodsAreSupported(t *testing.T) {
	for _, route := range gateway.DocumentedRoutes() {
		switch route.Method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		default:
			t.Errorf("unsupported documented method %q", route.Method)
		}
	}
}
