package modules

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/promptinjection"
)

type promptInput struct {
	ctx          context.Context
	text         strings.Builder
	limit, nodes int
	unscannable  bool
}

func (p *promptInput) add(text string) error {
	if !utf8.ValidString(text) || len(text)+1 > p.limit-p.text.Len() {
		return promptinjection.ErrUnavailable
	}
	if text != "" {
		p.text.WriteString(text)
		p.text.WriteByte('\n')
	}
	return nil
}
func (p *promptInput) walk(value any, depth int) error {
	p.nodes++
	if p.nodes > 65536 || depth > 64 {
		return promptinjection.ErrUnavailable
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	switch v := value.(type) {
	case nil:
		return nil
	case string:
		return p.add(v)
	case []any:
		for _, item := range v {
			if err := p.walk(item, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		kind, _ := v["type"].(string)
		switch kind {
		case "image_url", "input_image", "image", "computer_screenshot", "input_audio", "audio", "input_video", "video", "input_file_image_reference", "input_file_audio_reference", "input_file_video_reference":
			p.unscannable = true
			return nil
		case "input_file", "file", "input_file_reference", "document":
			return p.file(v, depth)
		}
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			switch key {
			case "type", "role", "id", "call_id", "name", "status", "annotations", "signature":
				continue
			}
			if err := p.add(key); err != nil {
				return err
			}
			if err := p.walk(v[key], depth+1); err != nil {
				return err
			}
		}
	case json.RawMessage:
		if len(v) > p.limit*4 {
			return promptinjection.ErrUnavailable
		}
		var decoded any
		if json.Unmarshal(v, &decoded) != nil {
			return promptinjection.ErrUnavailable
		}
		return p.walk(decoded, depth+1)
	default:
		// Transport-normalized content can contain typed slices or maps.
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) > p.limit*4 {
			return promptinjection.ErrUnavailable
		}
		var decoded any
		if json.Unmarshal(encoded, &decoded) != nil {
			return promptinjection.ErrUnavailable
		}
		switch decoded.(type) {
		case string, []any, map[string]any:
			return p.walk(decoded, depth+1)
		}
	}
	return nil
}
func (p *promptInput) file(file map[string]any, depth int) error {
	p.nodes++
	if p.nodes > 65536 || depth > 64 {
		return promptinjection.ErrUnavailable
	}
	if err := p.ctx.Err(); err != nil {
		return err
	}
	if nested, ok := file["file"].(map[string]any); ok {
		return p.file(nested, depth+1)
	}
	if source, ok := file["source"].(map[string]any); ok {
		kind, _ := source["type"].(string)
		if kind == "text" {
			return p.walk(source["data"], depth+1)
		}
		media, _ := source["media_type"].(string)
		data, _ := source["data"].(string)
		if kind == "base64" && strings.HasPrefix(media, "text/") {
			return p.decodeText(data, true)
		}
	}
	for _, key := range []string{"file_data", "data", "url", "file_url"} {
		data, ok := file[key].(string)
		if !ok {
			continue
		}
		if !strings.HasPrefix(data, "data:") {
			continue
		}
		header, payload, ok := strings.Cut(strings.TrimPrefix(data, "data:"), ",")
		if !ok {
			return promptinjection.ErrUnavailable
		}
		encoded := strings.HasSuffix(header, ";base64")
		header = strings.TrimSuffix(header, ";base64")
		media, _, err := mime.ParseMediaType(header)
		if err != nil {
			return promptinjection.ErrUnavailable
		}
		if strings.HasPrefix(media, "text/") {
			return p.decodeText(payload, encoded)
		}
	}
	p.unscannable = true
	return nil
}
func (p *promptInput) decodeText(data string, encoded bool) error {
	remaining := p.limit - p.text.Len()
	if len(data) > remaining*4 {
		return promptinjection.ErrUnavailable
	}
	var text []byte
	var err error
	if encoded {
		if base64.StdEncoding.DecodedLen(len(data)) > remaining {
			return promptinjection.ErrUnavailable
		}
		text, err = base64.StdEncoding.DecodeString(data)
	} else {
		var decoded string
		decoded, err = url.PathUnescape(data)
		text = []byte(decoded)
	}
	if err != nil {
		return promptinjection.ErrUnavailable
	}
	return p.add(string(text))
}

// PromptInjectionInput scans content only: identity, keys and request metadata
// never become classifier input. Binary files and unresolved references are explicit.
func PromptInjectionInput(ctx context.Context, req *RequestContext, limit int) (string, bool, error) {
	p := promptInput{ctx: ctx, limit: limit}
	for _, attachment := range req.Attachments {
		if strings.HasPrefix(attachment.MediaType, "text/") {
			if err := p.decodeText(attachment.Data, true); err != nil {
				return "", false, err
			}
		} else {
			p.unscannable = true
		}
	}
	values := []any{}
	for _, message := range req.Request.Messages {
		values = append(values, message.Content, message.ReasoningContent)
		if message.Audio != nil {
			p.unscannable = true
			values = append(values, message.Audio.Transcript)
		}
		if message.FunctionCall != nil {
			values = append(values, message.FunctionCall.Arguments)
		}
		for _, raw := range message.NativeContent {
			var block openai.BedrockContentBlock
			if json.Unmarshal(raw, &block) == nil && block.Document != nil {
				if strings.Contains(" csv html txt md ", " "+block.Document.Format+" ") {
					if err := p.decodeText(block.Document.Source.Bytes, true); err != nil {
						return "", p.unscannable, err
					}
				} else {
					p.unscannable = true
				}
			}
		}
		for _, call := range message.ToolCalls {
			values = append(values, call.Function.Arguments)
		}
		for _, block := range message.Reasoning {
			values = append(values, block.Thinking)
		}
		for _, meta := range message.AnthropicDocumentMetadata {
			values = append(values, meta.Title, meta.Context)
		}
	}
	for _, tool := range req.Request.Tools {
		values = append(values, tool.Function.Name, tool.Function.Description, tool.Function.Parameters)
	}
	for _, function := range req.Request.Functions {
		values = append(values, function.Name, function.Description, function.Parameters)
	}
	if req.ResponseRequest != nil {
		values = append(values, req.ResponseRequest.Instructions, req.ResponseRequest.Input)
		for _, tool := range req.ResponseRequest.Tools {
			values = append(values, tool.Name, tool.Description, tool.Parameters, tool.OutputSchema, tool.ServerDescription)
			if tool.Format != nil {
				values = append(values, tool.Format.Definition)
			}
		}
	}
	if req.CompletionRequest != nil {
		if _, ok := openai.EmbeddingInputStrings(req.CompletionRequest.Prompt); !ok {
			p.unscannable = true
		}
		values = append(values, req.CompletionRequest.Prompt)
	}
	if req.EmbeddingRequest != nil {
		if _, ok := openai.EmbeddingInputStrings(req.EmbeddingRequest.Input); !ok {
			p.unscannable = true
		}
		values = append(values, req.EmbeddingRequest.Input)
	}
	if req.RerankRequest != nil {
		values = append(values, req.RerankRequest.Query, req.RerankRequest.Documents)
	}
	if req.ModerationRequest != nil {
		values = append(values, req.ModerationRequest.Input)
	}
	if req.ImageGenerationRequest != nil {
		values = append(values, req.ImageGenerationRequest.Prompt)
	}
	if req.ImageEditRequest != nil {
		values = append(values, req.ImageEditRequest.Prompt)
		p.unscannable = true
	}
	if req.ImageVariationRequest != nil {
		p.unscannable = true
	}
	if req.AudioTranscriptionRequest != nil {
		values = append(values, req.AudioTranscriptionRequest.Prompt)
		p.unscannable = true
	}
	if req.AudioSpeechRequest != nil {
		values = append(values, req.AudioSpeechRequest.Input)
	}
	if req.SearchRequest != nil {
		values = append(values, req.SearchRequest.Query)
	}
	if req.OCRRequest != nil {
		values = append(values, req.OCRRequest.DocumentAnnotationPrompt)
		p.unscannable = true
	}
	for _, value := range values {
		if err := p.walk(value, 0); err != nil {
			return "", p.unscannable, err
		}
	}
	return p.text.String(), p.unscannable, nil
}
