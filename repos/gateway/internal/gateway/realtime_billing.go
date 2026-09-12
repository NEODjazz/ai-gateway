package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const maxRealtimePendingResponses = 16
const maxRealtimeConversationItems = 1024
const maxRealtimeDLPProjectionBytes = 64 << 10
const maxRealtimeCancelledResponses = 64
const maxRealtimeAudioAppendBytes = 15 << 20
const maxRealtimeAudioBufferBytes = 1 << 30

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
	cancelledResponses map[string]struct{}
	cancelledOrder     []string
	audioInputFormat   string
	audioBufferBytes   int
	pendingAudio       []int
	closed             bool
}

type realtimeBillingEntry struct {
	request       modules.RequestContext
	clientEventID string
	responseID    string
	settling      bool
}

type realtimeEventEnvelope struct {
	Type       string          `json:"type"`
	EventID    string          `json:"event_id"`
	ItemID     string          `json:"item_id"`
	Item       json.RawMessage `json:"item"`
	Session    json.RawMessage `json:"session"`
	Response   json.RawMessage `json:"response"`
	ResponseID string          `json:"response_id"`
	Audio      string          `json:"audio"`
	Delta      string          `json:"delta"`
}

func newRealtimeBillingTracker(pipeline modules.Pipeline, template modules.RequestContext, model string, admit func(context.Context, int) error) *realtimeBillingTracker {
	billing := pipeline.HasModule("billing")
	return &realtimeBillingTracker{
		pipeline: pipeline, template: template, model: model, enabled: billing || admit != nil, billing: billing, admit: admit,
		conversationItems: map[string]int{}, byResponse: map[string]*realtimeBillingEntry{},
		cancelledResponses: map[string]struct{}{},
		audioInputFormat:   "pcm16",
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
	if err := t.handleClientAudioEvent(ctx, event); err != nil {
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
	case "response.cancel":
		return t.cancelResponse(ctx, event.ResponseID, errors.New("realtime response cancelled by client"))
	}
	return nil
}

func (t *realtimeBillingTracker) handleClientAudioEvent(ctx context.Context, event realtimeEventEnvelope) error {
	switch event.Type {
	case "session.update":
		inputFormat, inputConfigured, outputConfigured, err := realtimeSessionAudioConfig(event.Session)
		if err != nil {
			return err
		}
		if inputConfigured && t.template.Metadata["provider.realtime_audio_input.enabled"] != "true" {
			return errors.New("selected realtime deployment does not support audio input")
		}
		if outputConfigured && t.template.Metadata["provider.realtime_audio_output.enabled"] != "true" {
			return errors.New("selected realtime deployment does not support audio output")
		}
		if inputFormat != "" {
			t.mu.Lock()
			defer t.mu.Unlock()
			if t.audioBufferBytes != 0 && inputFormat != t.audioInputFormat {
				return errors.New("realtime input audio format cannot change with buffered audio")
			}
			t.audioInputFormat = inputFormat
		}
	case "input_audio_buffer.append":
		if t.template.Metadata["provider.realtime_audio_input.enabled"] != "true" {
			return errors.New("selected realtime deployment does not support audio input")
		}
		decodedBytes, err := validRealtimeAudio(event.Audio)
		if err != nil {
			return err
		}
		t.mu.Lock()
		format := t.audioInputFormat
		t.mu.Unlock()
		if err := t.scanRealtimeAudio(ctx, format, event.Audio); err != nil {
			return err
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if decodedBytes > maxRealtimeAudioBufferBytes-t.audioBufferBytes {
			return errors.New("realtime input audio buffer exceeds limit")
		}
		t.audioBufferBytes += decodedBytes
	case "input_audio_buffer.clear":
		if t.template.Metadata["provider.realtime_audio_input.enabled"] != "true" {
			return errors.New("selected realtime deployment does not support audio input")
		}
		t.mu.Lock()
		t.audioBufferBytes = 0
		t.mu.Unlock()
	case "input_audio_buffer.commit":
		if t.template.Metadata["provider.realtime_audio_input.enabled"] != "true" {
			return errors.New("selected realtime deployment does not support audio input")
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if t.audioBufferBytes == 0 {
			return errors.New("realtime input audio buffer is empty")
		}
		if len(t.conversationItems)+t.unnamedItems >= maxRealtimeConversationItems {
			return errors.New("realtime conversation item limit exceeded")
		}
		tokens := realtimeAudioTokens(t.audioBufferBytes, t.audioInputFormat)
		t.audioBufferBytes = 0
		t.pendingAudio = append(t.pendingAudio, tokens)
		t.unnamedItems++
		t.conversationTokens = saturatedRealtimeTokens(t.conversationTokens, tokens)
	}
	return nil
}

func (t *realtimeBillingTracker) handleProviderAudioEvent(event realtimeEventEnvelope) error {
	switch event.Type {
	case "response.output_audio.delta", "response.audio.delta":
		if t.template.Metadata["provider.realtime_audio_output.enabled"] != "true" {
			return errors.New("selected realtime deployment returned unsupported audio output")
		}
		_, err := validRealtimeAudio(event.Delta)
		return err
	case "response.output_audio.done", "response.audio.done", "response.output_audio_transcript.delta", "response.output_audio_transcript.done", "response.audio_transcript.delta", "response.audio_transcript.done":
		if t.template.Metadata["provider.realtime_audio_output.enabled"] != "true" {
			return errors.New("selected realtime deployment returned unsupported audio output")
		}
	case "input_audio_buffer.committed":
		if t.template.Metadata["provider.realtime_audio_input.enabled"] != "true" || event.ItemID == "" || len(event.ItemID) > 256 {
			return errors.New("invalid realtime input audio commit")
		}
		t.mu.Lock()
		defer t.mu.Unlock()
		if _, exists := t.conversationItems[event.ItemID]; exists {
			return errors.New("duplicate realtime input audio item")
		}
		var tokens int
		if len(t.pendingAudio) > 0 {
			tokens = t.pendingAudio[0]
			t.pendingAudio = t.pendingAudio[1:]
			t.unnamedItems--
		} else if t.audioBufferBytes > 0 {
			if len(t.conversationItems)+t.unnamedItems >= maxRealtimeConversationItems {
				return errors.New("realtime conversation item limit exceeded")
			}
			tokens = realtimeAudioTokens(t.audioBufferBytes, t.audioInputFormat)
			t.audioBufferBytes = 0
			t.conversationTokens = saturatedRealtimeTokens(t.conversationTokens, tokens)
		}
		t.conversationItems[event.ItemID] = tokens
	case "input_audio_buffer.cleared":
		if t.template.Metadata["provider.realtime_audio_input.enabled"] != "true" {
			return errors.New("selected realtime deployment returned unsupported audio input event")
		}
		t.mu.Lock()
		t.audioBufferBytes = 0
		t.mu.Unlock()
	}
	return nil
}

func (t *realtimeBillingTracker) scanRealtimeAudio(ctx context.Context, format, audio string) error {
	if t.template.Metadata["provider.modules.av.enabled"] != "true" {
		return nil
	}
	request := cloneRealtimeBillingRequest(t.template)
	request.RequestID = newExecutionID()
	request.Attachments = []openai.ImageAttachment{{MediaType: realtimeAudioMediaType(format), Data: audio}}
	return t.pipeline.RunNamed(ctx, &request, "av")
}

func validRealtimeAudio(value string) (int, error) {
	if value == "" || len(value) > base64.StdEncoding.EncodedLen(maxRealtimeAudioAppendBytes) {
		return 0, errors.New("realtime audio chunk exceeds its size limit")
	}
	for index := range len(value) {
		character := value[index]
		if !(character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '+' || character == '/' || character == '=') {
			return 0, errors.New("realtime audio must be strict base64")
		}
	}
	decoded, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(value)))
	if err != nil || decoded <= 0 || decoded > maxRealtimeAudioAppendBytes {
		return 0, errors.New("realtime audio must be non-empty strict base64 within 15 MiB")
	}
	return int(decoded), nil
}

func realtimeAudioTokens(byteCount int, format string) int {
	bytesPerToken := 4800
	if format == "g711_ulaw" || format == "g711_alaw" {
		bytesPerToken = 800
	}
	return (byteCount + bytesPerToken - 1) / bytesPerToken
}

func realtimeAudioMediaType(format string) string {
	switch format {
	case "g711_ulaw":
		return "audio/pcmu"
	case "g711_alaw":
		return "audio/pcma"
	default:
		return "audio/pcm"
	}
}

func realtimeSessionAudioConfig(raw json.RawMessage) (string, bool, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, false, nil
	}
	var session struct {
		InputFormat      json.RawMessage `json:"input_audio_format"`
		OutputFormat     json.RawMessage `json:"output_audio_format"`
		Modalities       []string        `json:"modalities"`
		OutputModalities []string        `json:"output_modalities"`
		Audio            json.RawMessage `json:"audio"`
	}
	if json.Unmarshal(raw, &session) != nil {
		return "", false, false, errors.New("invalid realtime session audio configuration")
	}
	inputFormat, inputConfigured, err := parseRealtimeAudioFormat(session.InputFormat)
	if err != nil {
		return "", false, false, err
	}
	_, outputConfigured, err := parseRealtimeAudioFormat(session.OutputFormat)
	if err != nil {
		return "", false, false, err
	}
	for _, modality := range append(session.Modalities, session.OutputModalities...) {
		if modality == "audio" {
			outputConfigured = true
		}
	}
	if len(session.Audio) > 0 && string(session.Audio) != "null" {
		var audio struct {
			Input  json.RawMessage `json:"input"`
			Output json.RawMessage `json:"output"`
		}
		if json.Unmarshal(session.Audio, &audio) != nil {
			return "", false, false, errors.New("invalid realtime session audio configuration")
		}
		if len(audio.Input) > 0 && string(audio.Input) != "null" {
			inputConfigured = true
			var input struct {
				Format json.RawMessage `json:"format"`
			}
			if json.Unmarshal(audio.Input, &input) != nil {
				return "", false, false, errors.New("invalid realtime input audio configuration")
			}
			if nested, present, nestedErr := parseRealtimeAudioFormat(input.Format); nestedErr != nil {
				return "", false, false, nestedErr
			} else if present {
				if inputFormat != "" && inputFormat != nested {
					return "", false, false, errors.New("conflicting realtime input audio formats")
				}
				inputFormat = nested
			}
		}
		if len(audio.Output) > 0 && string(audio.Output) != "null" {
			outputConfigured = true
			var output struct {
				Format json.RawMessage `json:"format"`
			}
			if json.Unmarshal(audio.Output, &output) != nil {
				return "", false, false, errors.New("invalid realtime output audio configuration")
			}
			if _, _, err := parseRealtimeAudioFormat(output.Format); err != nil {
				return "", false, false, err
			}
		}
	}
	return inputFormat, inputConfigured, outputConfigured, nil
}

func parseRealtimeAudioFormat(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", false, nil
	}
	var legacy string
	if json.Unmarshal(raw, &legacy) == nil {
		switch legacy {
		case "pcm16", "g711_ulaw", "g711_alaw":
			return legacy, true, nil
		default:
			return "", false, errors.New("unsupported realtime audio format")
		}
	}
	var current struct {
		Type string `json:"type"`
		Rate int    `json:"rate,omitempty"`
	}
	if decodeStrictRealtimeJSON(raw, &current) != nil {
		return "", false, errors.New("invalid realtime audio format")
	}
	switch current.Type {
	case "audio/pcm":
		if current.Rate != 24000 {
			return "", false, errors.New("realtime PCM audio requires 24000 Hz")
		}
		return "pcm16", true, nil
	case "audio/pcmu":
		if current.Rate != 0 {
			return "", false, errors.New("realtime PCMU format must not set rate")
		}
		return "g711_ulaw", true, nil
	case "audio/pcma":
		if current.Rate != 0 {
			return "", false, errors.New("realtime PCMA format must not set rate")
		}
		return "g711_alaw", true, nil
	default:
		return "", false, errors.New("unsupported realtime audio format")
	}
}

func decodeStrictRealtimeJSON(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid trailing realtime configuration data")
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
	event, err := decodeRealtimeBillingEvent(payload)
	if err != nil {
		return err
	}
	if err := t.handleProviderAudioEvent(event); err != nil {
		return err
	}
	if !t.billing {
		return nil
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
		if _, cancelled := t.cancelledResponses[responseID]; cancelled {
			t.mu.Unlock()
			return nil
		}
		t.mu.Unlock()
		return errors.New("realtime response.done has no reserved request")
	}
	if entry.settling {
		t.mu.Unlock()
		return nil
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

func (t *realtimeBillingTracker) cancelResponse(ctx context.Context, responseID string, cause error) error {
	if !t.billing {
		return nil
	}
	if len(responseID) > 256 {
		return errors.New("realtime response ID is too long")
	}
	t.mu.Lock()
	var entry *realtimeBillingEntry
	if responseID != "" {
		entry = t.byResponse[responseID]
	} else {
		for index := len(t.pending) - 1; index >= 0; index-- {
			if t.pending[index].responseID != "" {
				entry = t.pending[index]
				responseID = entry.responseID
				break
			}
		}
	}
	if entry == nil {
		t.mu.Unlock()
		return nil
	}
	if entry.settling {
		t.mu.Unlock()
		return nil
	}
	entry.settling = true
	t.mu.Unlock()
	if err := t.pipeline.RunBillingLifecycle(ctx, &entry.request, "cancel", cause); err != nil {
		t.mu.Lock()
		entry.settling = false
		t.mu.Unlock()
		return err
	}
	t.mu.Lock()
	t.removePendingLocked(entry)
	t.markCancelledLocked(responseID)
	t.mu.Unlock()
	return nil
}

func (t *realtimeBillingTracker) markCancelledLocked(responseID string) {
	if responseID == "" {
		return
	}
	if _, found := t.cancelledResponses[responseID]; found {
		return
	}
	if len(t.cancelledOrder) >= maxRealtimeCancelledResponses {
		delete(t.cancelledResponses, t.cancelledOrder[0])
		t.cancelledOrder = t.cancelledOrder[1:]
	}
	t.cancelledResponses[responseID] = struct{}{}
	t.cancelledOrder = append(t.cancelledOrder, responseID)
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
			InputTokens       int `json:"input_tokens"`
			OutputTokens      int `json:"output_tokens"`
			TotalTokens       int `json:"total_tokens"`
			InputTokenDetails *struct {
				CachedTokens int `json:"cached_tokens"`
				TextTokens   int `json:"text_tokens"`
				AudioTokens  int `json:"audio_tokens"`
			} `json:"input_token_details"`
			OutputTokenDetails *struct {
				TextTokens  int `json:"text_tokens"`
				AudioTokens int `json:"audio_tokens"`
			} `json:"output_token_details"`
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
	result := &openai.Usage{PromptTokens: usage.InputTokens, CompletionTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens}
	if details := usage.InputTokenDetails; details != nil {
		if details.CachedTokens < 0 || details.TextTokens < 0 || details.AudioTokens < 0 || details.CachedTokens > usage.InputTokens || details.TextTokens > usage.InputTokens-details.AudioTokens {
			return "", nil, errors.New("invalid realtime input token details")
		}
		result.PromptTokensDetails = &openai.PromptTokenDetails{CachedTokens: details.CachedTokens, TextTokens: details.TextTokens, AudioTokens: details.AudioTokens}
	}
	if details := usage.OutputTokenDetails; details != nil {
		if details.TextTokens < 0 || details.AudioTokens < 0 || details.TextTokens > usage.OutputTokens-details.AudioTokens {
			return "", nil, errors.New("invalid realtime output token details")
		}
		result.CompletionTokensDetails = &openai.CompletionTokenDetails{TextTokens: details.TextTokens, AudioTokens: details.AudioTokens}
	}
	return value.ID, result, nil
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
	request.Attachments = append([]openai.ImageAttachment(nil), request.Attachments...)
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
