package provider

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
)

type lemonadeModelMetadata struct {
	ID         string   `json:"id"`
	Recipe     string   `json:"recipe"`
	Labels     []string `json:"labels"`
	Downloaded *bool    `json:"downloaded"`
	Embedding  bool     `json:"embedding"`
	Reranking  bool     `json:"reranking"`
	Vision     bool     `json:"vision"`
}

// Discovery uses installed models only. It neither downloads models nor infers
// a deployment mode from characteristic labels such as reasoning or vision.
func parseLemonadeModels(payload []byte) ([]DiscoveredModel, error) {
	var body struct {
		Data *[]lemonadeModelMetadata `json:"data"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, err
	}
	if body.Data == nil || len(*body.Data) > 10000 {
		return nil, errors.New("invalid Lemonade model list")
	}
	models := make([]DiscoveredModel, 0, len(*body.Data))
	seen := make(map[string]bool, len(*body.Data))
	for _, item := range *body.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || len(id) > 256 || strings.ContainsFunc(id, unicode.IsControl) {
			return nil, errors.New("invalid Lemonade model ID")
		}
		if seen[id] {
			return nil, errors.New("duplicate Lemonade model ID")
		}
		seen[id] = true
		if item.Downloaded != nil && !*item.Downloaded && item.Recipe != "cloud" {
			continue
		}
		capabilities, err := lemonadeModelCapabilities(item)
		if err != nil {
			return nil, err
		}
		models = append(models, DiscoveredModel{ID: id, Capabilities: capabilities, CapabilitySource: "provider_metadata"})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models, nil
}

func lemonadeModelCapabilities(item lemonadeModelMetadata) ([]string, error) {
	labels := make(map[string]bool, len(item.Labels))
	for _, label := range item.Labels {
		labels[label] = true
	}
	labels["embeddings"] = labels["embeddings"] || labels["embedding"] || item.Embedding
	labels["reranking"] = labels["reranking"] || item.Reranking
	labels["classification"] = labels["classification"] || labels["classifier"]
	labels["vision"] = labels["vision"] || item.Vision
	mode := ""
	for _, candidate := range []string{"chat", "embeddings", "reranking", "transcription", "image", "tts", "audio-generation", "classification", "3d"} {
		if labels[candidate] {
			if mode != "" {
				return nil, errors.New("Lemonade model declares multiple deployment modes")
			}
			mode = candidate
		}
	}
	modes := lemonadeRecipeModes(item.Recipe)
	if mode == "" && len(modes) > 0 {
		mode = modes[0]
	} else if mode != "" && len(modes) > 0 && !containsString(modes, mode) {
		return nil, errors.New("Lemonade recipe cannot serve the declared deployment mode")
	}
	capabilities := []string{}
	switch mode {
	case "chat":
		capabilities = append(capabilities, "chat", "completions", "stream")
		// Cloud and FLM explicitly reject Responses despite supporting Chat.
		switch item.Recipe {
		case "llamacpp", "ryzenai-llm", "vllm":
			capabilities = append(capabilities, "responses")
		}
		if labels["vision"] {
			capabilities = append(capabilities, "vision")
		}
		if labels["tool-calling"] {
			capabilities = append(capabilities, "tools")
		}
		if labels["chat-transcription"] {
			capabilities = append(capabilities, "audio_input")
		}
	case "embeddings":
		capabilities = append(capabilities, "embeddings")
	case "reranking":
		capabilities = append(capabilities, "rerank")
	case "transcription":
		capabilities = append(capabilities, "audio_transcription")
	case "image":
		if !labels["upscaling"] {
			capabilities = append(capabilities, "image_generation", "image_edit", "image_variation")
		}
	case "tts":
		capabilities = append(capabilities, "audio_speech")
	}
	return capabilities, nil
}

func lemonadeRecipeModes(recipe string) []string {
	switch recipe {
	case "llamacpp":
		return []string{"chat", "embeddings", "reranking"}
	case "flm":
		return []string{"chat", "embeddings", "transcription"}
	case "llamacpp-hrx", "ryzenai-llm", "vllm", "ds4", "cloud":
		return []string{"chat"}
	case "whispercpp", "moonshine":
		return []string{"transcription"}
	case "sd-cpp", "thenoise":
		return []string{"image"}
	case "kokoro":
		return []string{"tts"}
	case "openmoss":
		return []string{"tts", "audio-generation"}
	case "thinksound", "acestep":
		return []string{"audio-generation"}
	case "onnxruntime":
		return []string{"classification"}
	case "trellis":
		return []string{"3d"}
	default:
		return nil
	}
}
