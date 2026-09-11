package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const maxRealtimePendingResponses = 16
const maxRealtimeConversationItems = 1024
const maxRealtimeDLPProjectionBytes = 64 << 10

type realtimeBillingTracker struct {
	pipeline modules.Pipeline
	template modules.RequestContext
	model    string
	enabled  bool
	billing  bool
	admit    func(context.Context, int) error

	mu                 sync.Mutex
	sessionTokens      int
	conversationTokens int
	conversationItems  map[string]int
	unnamedItems       int
	pending            []*realtimeBillingEntry
	byResponse         map[string]*realtimeBillingEntry
	closed             bool
}

type realtimeBillingEntry struct {
	request       modules.RequestContext
	clientEventID string
	responseID    string
}

type realtimeEventEnvelope struct {
	Type     string          `json:"type"`
	EventID  string          `json:"event_id"`
	ItemID   string          `json:"item_id"`
	Item     json.RawMessage `json:"item"`
	Session  json.RawMessage `json:"session"`
	Response json.RawMessage `json:"response"`
}

func newRealtimeBillingTracker(pipeline modules.Pipeline, template modules.RequestContext, model string, admit func(context.Context, int) error) *realtimeBillingTracker {
	billing := pipeline.HasModule("billing")
	return &realtimeBillingTracker{
		pipeline: pipeline, template: template, model: model, enabled: billing || admit != nil, billing: billing, admit: admit,
		conversationItems: map[string]int{}, byResponse: map[string]*realtimeBillingEntry{},
	}
}

func (t *realtimeBillingTracker) ClientEvent(ctx context.Context, payload []byte) error {
	if !t.enabled {
		return nil
	}
	event, err := decodeRealtimeBillingEvent(payload)
	if err != nil {
		return err
	}
	if err := t.scanClientEvent(ctx, event); err != nil {
		return err
	}
	switch event.Type {
	case "session.update":
		t.mu.Lock()
		t.sessionTokens = realtimeRawTokens(event.Session)
		t.mu.Unlock()
	case "conversation.item.create":
		return t.addConversationItem(event)
	case "conversation.item.delete":
		t.deleteConversationItem(event.ItemID)
	case "response.create":
		return t.reserveResponse(ctx, event)
	}
	return nil
}

func (t *realtimeBillingTracker) scanClientEvent(ctx context.Context, event realtimeEventEnvelope) error {
	if t.template.Metadata["provider.modules.dlp.enabled"] != "true" {
		return nil
	}
	var value json.RawMessage
	switch event.Type {
	case "session.update":
		value = event.Session
	case "conversation.item.create":
		value = event.Item
	case "response.create":
		value = event.Response
	default:
		return nil
	}
	projection, err := realtimeTextProjection(value)
	if err != nil {
		return errors.Join(modules.ErrGuardrailUnavailable, err)
	}
	if projection == "" {
		return nil
	}
	request := cloneRealtimeBillingRequest(t.template)
	request.RequestID = newExecutionID()
	request.Request.Messages = []openai.Message{{Role: "user", Content: projection}}
	return t.pipeline.RunNamed(ctx, &request, "dlp")
}

func (t *realtimeBillingTracker) ProviderEvent(ctx context.Context, payload []byte) error {
	if !t.billing {
		return nil
	}
	event, err := decodeRealtimeBillingEvent(payload)
	if err != nil {
		return err
	}
	switch event.Type {
	case "response.created":
		responseID, _, err := realtimeResponseUsage(event.Response)
		if err != nil || responseID == "" {
			return errors.New("realtime response.created requires a response ID")
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		for _, pending := range t.pending {
			if pending.responseID == "" {
				pending.responseID = responseID
				t.byResponse[responseID] = pending
				return nil
			}
		}
		return errors.New("realtime response.created has no reserved request")
	case "response.done":
		return t.commitResponse(ctx, event.Response)
	case "error":
		return t.cancelClientEvent(ctx, event.EventID, errors.New("realtime provider rejected response"))
	}
	return nil
}

func (t *realtimeBillingTracker) Close(ctx context.Context, cause error) {
	if !t.billing {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	pending := append([]*realtimeBillingEntry(nil), t.pending...)
	t.pending = nil
	t.byResponse = map[string]*realtimeBillingEntry{}
	t.mu.Unlock()
	billingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, entry := range pending {
		_ = t.pipeline.RunBillingLifecycle(billingCtx, &entry.request, "cancel", cause)
	}
}

func (t *realtimeBillingTracker) addConversationItem(event realtimeEventEnvelope) error {
	if len(event.Item) == 0 {
		return errors.New("realtime conversation item is missing")
	}
	tokens := realtimeRawTokens(event.Item)
	itemID := realtimeItemID(event.Item)
	t.mu.Lock()
	defer t.mu.Unlock()
	if itemID == "" {
		if t.unnamedItems >= maxRealtimeConversationItems {
			return errors.New("realtime conversation item limit exceeded")
		}
		t.unnamedItems++
		t.conversationTokens = saturatedRealtimeTokens(t.conversationTokens, tokens)
		return nil
	}
	if len(itemID) > 256 {
		return errors.New("realtime conversation item ID is too long")
	}
	if _, found := t.conversationItems[itemID]; !found && len(t.conversationItems)+t.unnamedItems >= maxRealtimeConversationItems {
		return errors.New("realtime conversation item limit exceeded")
	}
	if previous, found := t.conversationItems[itemID]; found {
		t.conversationTokens -= previous
	}
	t.conversationItems[itemID] = tokens
	t.conversationTokens = saturatedRealtimeTokens(t.conversationTokens, tokens)
	return nil
}

func (t *realtimeBillingTracker) deleteConversationItem(itemID string) {
	if itemID == "" {
		return
	}
	t.mu.Lock()
	if tokens, found := t.conversationItems[itemID]; found {
		t.conversationTokens -= tokens
		delete(t.conversationItems, itemID)
	}
	t.mu.Unlock()
}

func (t *realtimeBillingTracker) reserveResponse(ctx context.Context, event realtimeEventEnvelope) error {
	maxOutput, err := realtimeMaxOutputTokens(event.Response)
	if err != nil {
		return err
	}
	t.mu.Lock()
	if t.closed || len(t.pending) >= maxRealtimePendingResponses {
		t.mu.Unlock()
		return errors.New("realtime pending response limit exceeded")
	}
	inputTokens := saturatedRealtimeTokens(t.sessionTokens, t.conversationTokens)
	inputTokens = saturatedRealtimeTokens(inputTokens, realtimeRawTokens(event.Response))
	t.mu.Unlock()
	reserveTokens := openai.ReserveTokens(inputTokens, maxOutput)
	if t.admit != nil {
		if err := t.admit(ctx, reserveTokens); err != nil {
			return err
		}
	}
	if !t.billing {
		return nil
	}

	request := cloneRealtimeBillingRequest(t.template)
	request.RequestID = newExecutionID()
	request.Request.Model = t.model
	request.Request.Messages = nil
	request.Usage = &openai.Usage{PromptTokens: inputTokens, CompletionTokens: maxOutput, TotalTokens: reserveTokens}
	request.Metadata["gateway.api_type"] = "realtime"
	request.Metadata["gateway.realtime_usage_exact"] = "false"
	if err := t.pipeline.RunBillingLifecycle(ctx, &request, "reserve", nil); err != nil {
		return err
	}
	entry := &realtimeBillingEntry{request: request, clientEventID: event.EventID}
	t.mu.Lock()
	if t.closed || len(t.pending) >= maxRealtimePendingResponses {
		t.mu.Unlock()
		_ = t.pipeline.RunBillingLifecycle(ctx, &entry.request, "cancel", errors.New("realtime session closed during reserve"))
		return errors.New("realtime pending response limit exceeded")
	}
	t.pending = append(t.pending, entry)
	t.mu.Unlock()
	return nil
}

func (t *realtimeBillingTracker) commitResponse(ctx context.Context, response json.RawMessage) error {
	responseID, usage, err := realtimeResponseUsage(response)
	if err != nil || responseID == "" {
		return errors.New("realtime response.done has invalid usage")
	}
	t.mu.Lock()
	entry := t.byResponse[responseID]
	if entry == nil {
		t.mu.Unlock()
		return errors.New("realtime response.done has no reserved request")
	}
	t.removePendingLocked(entry)
	t.mu.Unlock()
	if usage != nil {
		entry.request.Usage = usage
		entry.request.Metadata["gateway.realtime_usage_exact"] = "true"
	}
	if err := t.pipeline.RunBillingLifecycle(ctx, &entry.request, "commit", nil); err != nil {
		_ = t.pipeline.RunBillingLifecycle(ctx, &entry.request, "cancel", err)
		return err
	}
	return nil
}

func (t *realtimeBillingTracker) cancelClientEvent(ctx context.Context, eventID string, cause error) error {
	if eventID == "" {
		return nil
	}
	t.mu.Lock()
	for _, entry := range t.pending {
		if entry.clientEventID == eventID {
			t.removePendingLocked(entry)
			t.mu.Unlock()
			return t.pipeline.RunBillingLifecycle(ctx, &entry.request, "cancel", cause)
		}
	}
	t.mu.Unlock()
	return nil
}

func (t *realtimeBillingTracker) removePendingLocked(entry *realtimeBillingEntry) {
	delete(t.byResponse, entry.responseID)
	for index, pending := range t.pending {
		if pending == entry {
			t.pending = append(t.pending[:index], t.pending[index+1:]...)
			return
		}
	}
}

func decodeRealtimeBillingEvent(payload []byte) (realtimeEventEnvelope, error) {
	var event realtimeEventEnvelope
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if err := decoder.Decode(&event); err != nil || event.Type == "" {
		return event, errors.New("invalid realtime event")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return event, errors.New("invalid trailing realtime event data")
	}
	return event, nil
}

func realtimeRawTokens(value json.RawMessage) int {
	if len(value) == 0 || string(value) == "null" {
		return 0
	}
	return openai.EstimateContextTokens(value)
}

func realtimeTextProjection(payload json.RawMessage) (string, error) {
	if len(payload) == 0 || string(payload) == "null" {
		return "", nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", errors.New("invalid realtime policy payload")
	}
	var result bytes.Buffer
	items := 0
	var appendValue func(any, int, string) error
	appendValue = func(current any, depth int, key string) error {
		if depth > 32 || items > 4096 {
			return errors.New("realtime policy payload is too complex")
		}
		items++
		switch typed := current.(type) {
		case string:
			switch key {
			case "audio", "data", "image", "image_url":
				return nil
			}
			if typed == "" {
				return nil
			}
			if result.Len() > 0 {
				result.WriteByte('\n')
			}
			if len(typed) > maxRealtimeDLPProjectionBytes-result.Len() {
				return errors.New("realtime text projection exceeds 64 KiB")
			}
			result.WriteString(typed)
		case []any:
			for _, item := range typed {
				if err := appendValue(item, depth+1, key); err != nil {
					return err
				}
			}
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for childKey := range typed {
				keys = append(keys, childKey)
			}
			sort.Strings(keys)
			for _, childKey := range keys {
				child := typed[childKey]
				if err := appendValue(child, depth+1, childKey); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := appendValue(value, 0, ""); err != nil {
		return "", err
	}
	return result.String(), nil
}

func realtimeItemID(item json.RawMessage) string {
	var value struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(item, &value)
	return value.ID
}

func realtimeMaxOutputTokens(response json.RawMessage) (int, error) {
	if len(response) == 0 || string(response) == "null" {
		return openai.DefaultOutputTokenReserve, nil
	}
	var value struct {
		MaxOutputTokens json.RawMessage `json:"max_output_tokens"`
	}
	if json.Unmarshal(response, &value) != nil {
		return 0, errors.New("invalid realtime response configuration")
	}
	if len(value.MaxOutputTokens) == 0 || string(value.MaxOutputTokens) == `"inf"` {
		return openai.DefaultOutputTokenReserve, nil
	}
	var tokens int
	if json.Unmarshal(value.MaxOutputTokens, &tokens) != nil || tokens < 1 || tokens > 1_000_000 {
		return 0, errors.New("invalid realtime max_output_tokens")
	}
	return tokens, nil
}

func realtimeResponseUsage(response json.RawMessage) (string, *openai.Usage, error) {
	var value struct {
		ID    string `json:"id"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if len(response) == 0 || json.Unmarshal(response, &value) != nil || len(value.ID) > 256 {
		return "", nil, errors.New("invalid realtime response")
	}
	if value.Usage == nil {
		return value.ID, nil, nil
	}
	usage := value.Usage
	if usage.InputTokens < 0 || usage.OutputTokens < 0 || usage.TotalTokens < 0 || usage.InputTokens > math.MaxInt-usage.OutputTokens || usage.TotalTokens != usage.InputTokens+usage.OutputTokens {
		return "", nil, errors.New("invalid realtime response usage")
	}
	return value.ID, &openai.Usage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}, nil
}

func saturatedRealtimeTokens(current, increment int) int {
	if increment < 0 || current > math.MaxInt-increment {
		return math.MaxInt
	}
	return current + increment
}

func cloneRealtimeBillingRequest(request modules.RequestContext) modules.RequestContext {
	request.Tags = append([]string(nil), request.Tags...)
	request.Roles = append([]string(nil), request.Roles...)
	request.AccessGroupIDs = append([]string(nil), request.AccessGroupIDs...)
	request.Metadata = cloneRealtimeMetadata(request.Metadata)
	return request
}

func cloneRealtimeMetadata(metadata map[string]string) map[string]string {
	cloned := make(map[string]string, len(metadata)+2)
	for key, value := range metadata {
		cloned[key] = value
	}
	return cloned
}
