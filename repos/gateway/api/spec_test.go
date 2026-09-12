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
