package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-gateway-gateway/internal/openai"
)

const (
	maxBedrockEventBytes  = 16 << 20
	maxBedrockStreamBytes = 64 << 20
)

type bedrockStreamBlock struct {
	started            bool
	tool               *bedrockToolUse
	toolInput          strings.Builder
	text               strings.Builder
	citations          []bedrockCitation
	reasoningType      string
	reasoningText      strings.Builder
	reasoningSignature strings.Builder
	redactedReasoning  strings.Builder
}

type bedrockStreamState struct {
	model       string
	response    bedrockResponse
	blocks      map[int]*bedrockStreamBlock
	messageSeen bool
	messageStop bool
	metadata    bool
	written     int
}

func (b Bedrock) StreamChatCompletions(ctx context.Context, request openai.ChatCompletionRequest, write ChatCompletionStreamWriter) (openai.ChatCompletionResponse, error) {
	if !request.Stream {
		return openai.ChatCompletionResponse{}, bedrockInvalid("stream")
	}
	body, err := bedrockChatRequest(request)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	endpoint := b.baseURL + "/model/" + url.PathEscape(request.Model) + "/converse-stream"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if err := b.authorize(ctx, httpRequest, payload); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	response, err := b.client.Do(httpRequest)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return openai.ChatCompletionResponse{}, responseStatusError("bedrock", response)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(contentType), "application/vnd.amazon.eventstream") {
		return openai.ChatCompletionResponse{}, errors.New("Bedrock stream has an invalid content type")
	}
	state := bedrockStreamState{model: request.Model, blocks: make(map[int]*bedrockStreamBlock)}
	if err := readBedrockEventStream(response.Body, state.handle(write)); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if !state.messageSeen || !state.messageStop || !state.metadata || len(state.blocks) != 0 {
		return openai.ChatCompletionResponse{}, errors.New("Bedrock stream ended before completion")
	}
	if len(state.response.AdditionalModelResponseFields) > 0 && string(state.response.AdditionalModelResponseFields) != "null" && len(request.BedrockAdditionalModelResponseFieldPaths) == 0 {
		return openai.ChatCompletionResponse{}, errors.New("Bedrock returned unrequested additional model response fields")
	}
	if len(state.response.Trace) > 0 && string(state.response.Trace) != "null" {
		traceEnabled := request.BedrockGuardrailConfig != nil && (request.BedrockGuardrailConfig.Trace == "enabled" || request.BedrockGuardrailConfig.Trace == "enabled_full")
		if !traceEnabled || len(state.response.Trace) > 1<<20 || !json.Valid(state.response.Trace) || bytes.Equal(bytes.TrimSpace(state.response.Trace), []byte("null")) {
			return openai.ChatCompletionResponse{}, errors.New("Bedrock returned invalid or unrequested guardrail trace")
		}
	}
	result, err := bedrockToChat(state.response, request.Model)
	if err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	if len(result.Choices) != 1 {
		return openai.ChatCompletionResponse{}, errors.New("Bedrock stream produced an invalid result")
	}
	finish := result.Choices[0].FinishReason
	chunk := map[string]any{"id": result.ID, "object": "chat.completion.chunk", "model": result.Model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}, "usage": result.Usage}
	if err := writeBedrockChatChunk(write, chunk); err != nil {
		return openai.ChatCompletionResponse{}, err
	}
	return result, nil
}

func (s *bedrockStreamState) handle(write ChatCompletionStreamWriter) func(map[string]string, []byte) error {
	return func(headers map[string]string, payload []byte) error {
		messageType := headers[":message-type"]
		if messageType == "exception" {
			exceptionType := headers[":exception-type"]
			class, status := FailureUnavailable, http.StatusServiceUnavailable
			switch exceptionType {
			case "validationException":
				class, status = FailureClientRequest, http.StatusBadRequest
			case "throttlingException":
				class, status = FailureRateLimit, http.StatusTooManyRequests
			case "modelStreamErrorException":
				status = http.StatusFailedDependency
			case "internalServerException", "serviceUnavailableException":
			default:
				return errors.New("unknown Bedrock stream exception")
			}
			return &Error{Class: class, Provider: "bedrock", StatusCode: status, UpstreamCode: exceptionType, Err: fmt.Errorf("Bedrock stream exception: %s", exceptionType)}
		}
		if messageType != "event" || headers[":content-type"] != "application/json" {
			return errors.New("invalid Bedrock event stream headers")
		}
		switch headers[":event-type"] {
		case "messageStart":
			if s.messageSeen || s.messageStop {
				return errors.New("duplicate Bedrock message start")
			}
			var event struct {
				Role string `json:"role"`
			}
			if json.Unmarshal(payload, &event) != nil || event.Role != "assistant" {
				return errors.New("invalid Bedrock message start")
			}
			s.messageSeen = true
			s.response.Output.Message.Role = event.Role
			return writeBedrockChatChunk(write, map[string]any{"id": "", "object": "chat.completion.chunk", "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
		case "contentBlockStart":
			if !s.messageSeen || s.messageStop {
				return errors.New("Bedrock content block started outside a message")
			}
			var event struct {
				Index int `json:"contentBlockIndex"`
				Start struct {
					ToolUse *struct {
						ID   string `json:"toolUseId"`
						Name string `json:"name"`
					} `json:"toolUse"`
				} `json:"start"`
			}
			if json.Unmarshal(payload, &event) != nil || event.Index < 0 || event.Index >= 128 || s.blocks[event.Index] != nil {
				return errors.New("invalid Bedrock content block start")
			}
			block := &bedrockStreamBlock{started: true}
			if event.Start.ToolUse != nil {
				if !validBedrockToolUseID(event.Start.ToolUse.ID) || !openai.ValidBedrockToolName(event.Start.ToolUse.Name) {
					return errors.New("invalid Bedrock tool start")
				}
				block.tool = &bedrockToolUse{ID: event.Start.ToolUse.ID, Name: event.Start.ToolUse.Name}
				index := event.Index
				if err := writeBedrockChatChunk(write, map[string]any{"id": "", "object": "chat.completion.chunk", "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": block.tool.ID, "type": "function", "function": map[string]any{"name": block.tool.Name, "arguments": ""}}}}, "finish_reason": nil}}}); err != nil {
					return err
				}
			}
			s.blocks[event.Index] = block
			return nil
		case "contentBlockDelta":
			return s.contentDelta(payload, write)
		case "contentBlockStop":
			return s.contentStop(payload)
		case "messageStop":
			if !s.messageSeen || s.messageStop || len(s.blocks) != 0 {
				return errors.New("invalid Bedrock message stop")
			}
			var event struct {
				StopReason                    string          `json:"stopReason"`
				AdditionalModelResponseFields json.RawMessage `json:"additionalModelResponseFields"`
			}
			if json.Unmarshal(payload, &event) != nil || event.StopReason == "" {
				return errors.New("invalid Bedrock message stop")
			}
			s.messageStop = true
			s.response.StopReason = event.StopReason
			s.response.AdditionalModelResponseFields = append(json.RawMessage(nil), event.AdditionalModelResponseFields...)
			return nil
		case "metadata":
			if !s.messageStop || s.metadata {
				return errors.New("invalid Bedrock stream metadata order")
			}
			var event struct {
				Usage *struct {
					InputTokens  int `json:"inputTokens"`
					OutputTokens int `json:"outputTokens"`
					TotalTokens  int `json:"totalTokens"`
				} `json:"usage"`
				Trace json.RawMessage `json:"trace"`
			}
			if json.Unmarshal(payload, &event) != nil || event.Usage == nil {
				return errors.New("invalid Bedrock stream metadata")
			}
			s.metadata = true
			s.response.Usage = event.Usage
			s.response.Trace = append(json.RawMessage(nil), event.Trace...)
			return nil
		default:
			return errors.New("unknown Bedrock event stream event")
		}
	}
}

func (s *bedrockStreamState) contentDelta(payload []byte, write ChatCompletionStreamWriter) error {
	var event struct {
		Index int `json:"contentBlockIndex"`
		Delta struct {
			Text    *string `json:"text"`
			ToolUse *struct {
				Input string `json:"input"`
			} `json:"toolUse"`
			Citation         *bedrockCitation `json:"citation"`
			ReasoningContent *struct {
				Text            *string `json:"text"`
				Signature       *string `json:"signature"`
				RedactedContent *string `json:"redactedContent"`
			} `json:"reasoningContent"`
		} `json:"delta"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return errors.New("invalid Bedrock content delta")
	}
	block := s.blocks[event.Index]
	if block == nil {
		return errors.New("Bedrock content delta has no active block")
	}
	members := 0
	if event.Delta.Text != nil {
		members++
		if block.tool != nil || block.reasoningType != "" || len(*event.Delta.Text) > maxBedrockStreamBytes-s.written {
			return errors.New("invalid Bedrock text delta")
		}
		s.written += len(*event.Delta.Text)
		block.text.WriteString(*event.Delta.Text)
		if err := writeBedrockChatChunk(write, map[string]any{"id": "", "object": "chat.completion.chunk", "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": *event.Delta.Text}, "finish_reason": nil}}}); err != nil {
			return err
		}
	}
	if event.Delta.ToolUse != nil {
		members++
		if block.tool == nil || len(event.Delta.ToolUse.Input) > maxBedrockStreamBytes-s.written {
			return errors.New("invalid Bedrock tool delta")
		}
		s.written += len(event.Delta.ToolUse.Input)
		block.toolInput.WriteString(event.Delta.ToolUse.Input)
		index := event.Index
		if err := writeBedrockChatChunk(write, map[string]any{"id": "", "object": "chat.completion.chunk", "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": event.Delta.ToolUse.Input}}}}, "finish_reason": nil}}}); err != nil {
			return err
		}
	}
	if event.Delta.Citation != nil {
		members++
		if block.tool != nil || block.reasoningType != "" || len(block.citations) >= 128 {
			return errors.New("invalid Bedrock citation delta")
		}
		block.citations = append(block.citations, *event.Delta.Citation)
	}
	if event.Delta.ReasoningContent != nil {
		members++
		reasoning := event.Delta.ReasoningContent
		reasoningMembers := 0
		if reasoning.Text != nil {
			reasoningMembers++
			if block.tool != nil || block.text.Len() > 0 || len(block.citations) > 0 || block.reasoningType == "redacted_thinking" || len(*reasoning.Text) > maxBedrockStreamBytes-s.written {
				return errors.New("invalid Bedrock reasoning text delta")
			}
			block.reasoningType = "thinking"
			block.reasoningText.WriteString(*reasoning.Text)
			s.written += len(*reasoning.Text)
		}
		if reasoning.Signature != nil {
			reasoningMembers++
			if block.tool != nil || block.text.Len() > 0 || len(block.citations) > 0 || block.reasoningType == "redacted_thinking" || len(*reasoning.Signature) > maxBedrockStreamBytes-s.written {
				return errors.New("invalid Bedrock reasoning signature delta")
			}
			block.reasoningType = "thinking"
			block.reasoningSignature.WriteString(*reasoning.Signature)
			s.written += len(*reasoning.Signature)
		}
		if reasoning.RedactedContent != nil {
			reasoningMembers++
			if block.tool != nil || block.text.Len() > 0 || len(block.citations) > 0 || block.reasoningType != "" || len(*reasoning.RedactedContent) > maxBedrockStreamBytes-s.written {
				return errors.New("invalid Bedrock redacted reasoning delta")
			}
			block.reasoningType = "redacted_thinking"
			block.redactedReasoning.WriteString(*reasoning.RedactedContent)
			s.written += len(*reasoning.RedactedContent)
		}
		if reasoningMembers != 1 {
			return errors.New("Bedrock reasoning delta must contain one union member")
		}
		index := event.Index
		delta := openai.ReasoningBlock{Index: &index, Type: block.reasoningType}
		if reasoning.Text != nil {
			delta.Thinking = *reasoning.Text
		}
		if reasoning.Signature != nil {
			delta.Signature = *reasoning.Signature
		}
		if reasoning.RedactedContent != nil {
			delta.Data = *reasoning.RedactedContent
		}
		if err := writeBedrockChatChunk(write, map[string]any{"id": "", "object": "chat.completion.chunk", "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"reasoning": []openai.ReasoningBlock{delta}}, "finish_reason": nil}}}); err != nil {
			return err
		}
	}
	if members != 1 {
		return errors.New("Bedrock content delta must contain one union member")
	}
	return nil
}

func (s *bedrockStreamState) contentStop(payload []byte) error {
	var event struct {
		Index int `json:"contentBlockIndex"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return errors.New("invalid Bedrock content block stop")
	}
	block := s.blocks[event.Index]
	if block == nil {
		return errors.New("Bedrock content block stop has no active block")
	}
	if block.tool != nil {
		if block.toolInput.Len() == 0 || !json.Valid([]byte(block.toolInput.String())) {
			return errors.New("invalid Bedrock streamed tool input")
		}
		if json.Unmarshal([]byte(block.toolInput.String()), &block.tool.Input) != nil {
			return errors.New("invalid Bedrock streamed tool input")
		}
		s.response.Output.Message.Content = append(s.response.Output.Message.Content, bedrockContentBlock{ToolUse: block.tool})
	} else if block.reasoningType != "" {
		index := event.Index
		reasoning := openai.ReasoningBlock{Index: &index, Type: block.reasoningType}
		content := bedrockReasoningContent{}
		if block.reasoningType == "thinking" {
			reasoning.Thinking = block.reasoningText.String()
			reasoning.Signature = block.reasoningSignature.String()
			content.ReasoningText = &bedrockReasoningText{Text: reasoning.Thinking, Signature: reasoning.Signature}
		} else {
			reasoning.Data = block.redactedReasoning.String()
			content.RedactedContent = reasoning.Data
		}
		if err := openai.ValidateBedrockReasoningBlocks([]openai.ReasoningBlock{reasoning}); err != nil {
			return fmt.Errorf("invalid Bedrock streamed reasoning content: %w", err)
		}
		if reasoning.Type == "redacted_thinking" {
			if _, err := base64.StdEncoding.DecodeString(reasoning.Data); err != nil {
				return errors.New("invalid Bedrock streamed redacted reasoning content")
			}
		}
		s.response.Output.Message.Content = append(s.response.Output.Message.Content, bedrockContentBlock{ReasoningContent: &content})
	} else {
		text := block.text.String()
		if text == "" {
			return errors.New("empty Bedrock streamed content block")
		}
		if len(block.citations) > 0 {
			s.response.Output.Message.Content = append(s.response.Output.Message.Content, bedrockContentBlock{CitationsContent: &bedrockCitationsContent{Content: []bedrockCitationText{{Text: &text}}, Citations: block.citations}})
		} else {
			s.response.Output.Message.Content = append(s.response.Output.Message.Content, bedrockContentBlock{Text: text})
		}
	}
	delete(s.blocks, event.Index)
	return nil
}

func writeBedrockChatChunk(write ChatCompletionStreamWriter, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return write(string(payload))
}

func validBedrockToolUseID(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_.:-", char) {
			continue
		}
		return false
	}
	return true
}

func readBedrockEventStream(reader io.Reader, handle func(map[string]string, []byte) error) error {
	totalRead := 0
	for {
		prelude := make([]byte, 12)
		if _, err := io.ReadFull(reader, prelude); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		total := int(binary.BigEndian.Uint32(prelude[:4]))
		headerLength := int(binary.BigEndian.Uint32(prelude[4:8]))
		if total < 16 || total > maxBedrockEventBytes || headerLength > total-16 || binary.BigEndian.Uint32(prelude[8:12]) != crc32.ChecksumIEEE(prelude[:8]) || total > maxBedrockStreamBytes-totalRead {
			return errors.New("invalid Bedrock event stream prelude")
		}
		rest := make([]byte, total-12)
		if _, err := io.ReadFull(reader, rest); err != nil {
			return err
		}
		message := append(prelude, rest...)
		if binary.BigEndian.Uint32(message[total-4:]) != crc32.ChecksumIEEE(message[:total-4]) {
			return errors.New("invalid Bedrock event stream checksum")
		}
		headers, err := decodeBedrockEventHeaders(message[12 : 12+headerLength])
		if err != nil {
			return err
		}
		if err := handle(headers, message[12+headerLength:total-4]); err != nil {
			return err
		}
		totalRead += total
	}
}

func decodeBedrockEventHeaders(data []byte) (map[string]string, error) {
	headers := make(map[string]string)
	for len(data) > 0 {
		nameLength := int(data[0])
		if nameLength == 0 || len(data) < 2+nameLength {
			return nil, errors.New("invalid Bedrock event stream header")
		}
		name := string(data[1 : 1+nameLength])
		if _, duplicate := headers[name]; duplicate {
			return nil, errors.New("duplicate Bedrock event stream header")
		}
		typeIndex := 1 + nameLength
		headerType := data[typeIndex]
		data = data[typeIndex+1:]
		var valueLength int
		switch headerType {
		case 0, 1:
			valueLength = 0
		case 2:
			valueLength = 1
		case 3:
			valueLength = 2
		case 4:
			valueLength = 4
		case 5, 8:
			valueLength = 8
		case 6, 7:
			if len(data) < 2 {
				return nil, errors.New("invalid Bedrock event stream header length")
			}
			valueLength = int(binary.BigEndian.Uint16(data[:2]))
			data = data[2:]
		case 9:
			valueLength = 16
		default:
			return nil, errors.New("unknown Bedrock event stream header type")
		}
		if valueLength > len(data) {
			return nil, errors.New("truncated Bedrock event stream header")
		}
		if headerType == 7 {
			headers[name] = string(data[:valueLength])
		} else {
			headers[name] = ""
		}
		data = data[valueLength:]
	}
	return headers, nil
}
