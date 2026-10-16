package provider

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLemonadeModelCapabilities(t *testing.T) {
	for _, test := range []struct {
		name  string
		model lemonadeModelMetadata
		want  []string
	}{
		{"chat characteristics", lemonadeModelMetadata{Recipe: "llamacpp", Labels: []string{"chat", "reasoning", "vision", "tool-calling", "chat-transcription", "realtime-transcription"}}, []string{"chat", "stream", "responses", "vision", "tools", "audio_input"}},
		{"flm rejects responses", lemonadeModelMetadata{Recipe: "flm"}, []string{"chat", "stream"}},
		{"cloud rejects responses", lemonadeModelMetadata{Recipe: "cloud"}, []string{"chat", "stream"}},
		{"legacy chat recipe", lemonadeModelMetadata{Recipe: "llamacpp", Labels: []string{"reasoning"}}, []string{"chat", "stream", "responses"}},
		{"unknown recipe characteristics", lemonadeModelMetadata{Recipe: "future", Labels: []string{"reasoning", "vision", "tool-calling"}}, []string{}},
		{"embedding alias", lemonadeModelMetadata{Recipe: "llamacpp", Labels: []string{"embedding", "embeddings", "vision"}}, []string{"embeddings"}},
		{"legacy embedding boolean", lemonadeModelMetadata{Recipe: "llamacpp", Embedding: true}, []string{"embeddings"}},
		{"reranking", lemonadeModelMetadata{Recipe: "llamacpp", Reranking: true}, []string{"rerank"}},
		{"whisper default", lemonadeModelMetadata{Recipe: "whispercpp", Labels: []string{"realtime-transcription"}}, []string{"audio_transcription"}},
		{"explicit transcription", lemonadeModelMetadata{Recipe: "flm", Labels: []string{"transcription"}}, []string{"audio_transcription"}},
		{"image default", lemonadeModelMetadata{Recipe: "sd-cpp"}, []string{"image_generation", "image_edit", "image_variation"}},
		{"upscaler", lemonadeModelMetadata{Recipe: "sd-cpp", Labels: []string{"image", "upscaling"}}, []string{}},
		{"tts default", lemonadeModelMetadata{Recipe: "kokoro"}, []string{"audio_speech"}},
		{"explicit tts", lemonadeModelMetadata{Labels: []string{"tts"}}, []string{"audio_speech"}},
		{"classification", lemonadeModelMetadata{Labels: []string{"classifier", "classification"}}, []string{}},
		{"audio generation", lemonadeModelMetadata{Labels: []string{"audio-generation"}}, []string{}},
		{"mesh", lemonadeModelMetadata{Labels: []string{"3d"}}, []string{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := lemonadeModelCapabilities(test.model)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("capabilities=%v err=%v; want %v", got, err, test.want)
			}
		})
	}
}

func TestParseLemonadeModels(t *testing.T) {
	payload := []byte(`{"object":"list","data":[
		{"id":"z-chat","recipe":"llamacpp","labels":["chat","tool-calling"],"downloaded":true},
		{"id":"not-installed","recipe":"llamacpp","downloaded":false},
		{"id":"cloud-chat","recipe":"cloud","cloud_provider":"example","downloaded":false},
		{"id":"a-embedding","recipe":"llamacpp","labels":["embeddings"],"downloaded":true},
		{"id":"unknown","recipe":"future","labels":["reasoning"]}
	]}`)
	models, err := parseDiscoveredModels("lemonade", payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 4 || models[0].ID != "a-embedding" || models[1].ID != "cloud-chat" || models[2].ID != "unknown" || models[3].ID != "z-chat" {
		t.Fatalf("unexpected installed model list: %+v", models)
	}
	for _, model := range models {
		if model.CapabilitySource != "provider_metadata" {
			t.Fatalf("metadata source omitted for %s", model.ID)
		}
	}
	if len(models[2].Capabilities) != 0 || !reflect.DeepEqual(models[0].Capabilities, []string{"embeddings"}) {
		t.Fatalf("mode or unknown capabilities were expanded: %+v", models)
	}
}

func TestParseLemonadeModelsRejectsInvalidMetadata(t *testing.T) {
	for _, payload := range []string{
		`{}`, `{"data":null}`, `{"data":{}}`, `{"data":[null]}`,
		`{"data":[{"id":""}]}`, `{"data":[{"id":"bad\nname"}]}`,
		`{"data":[{"id":"duplicate"},{"id":" duplicate "}]}`,
		`{"data":[{"id":"mixed","labels":["chat","embeddings"]}]}`,
		`{"data":[{"id":"mixed","labels":["chat"],"embedding":true}]}`,
		`{"data":[{"id":"mixed","embedding":true,"reranking":true}]}`,
		`{"data":[{"id":"wrong-mode","recipe":"llamacpp","labels":["classification"]}]}`,
		`{"data":[{"id":"wrong-mode","recipe":"kokoro","labels":["chat"]}]}`,
		`{"data":[{"id":"bad-label","labels":[42]}]}`,
		`{"data":[{"id":"bad-download","downloaded":"true"}]}`,
	} {
		if _, err := parseLemonadeModels([]byte(payload)); err == nil {
			t.Fatalf("accepted invalid metadata: %s", payload)
		}
	}
	for _, count := range []int{256, 257} {
		payload, err := json.Marshal(map[string]any{"data": []map[string]string{{"id": strings.Repeat("x", count)}}})
		if err != nil {
			t.Fatal(err)
		}
		_, err = parseLemonadeModels(payload)
		if (err != nil) != (count > 256) {
			t.Fatalf("ID length %d: %v", count, err)
		}
	}
	if _, err := parseLemonadeModels([]byte(`{"data":[]}`)); err != nil {
		t.Fatalf("valid empty model list: %v", err)
	}
	entries := make([]lemonadeModelMetadata, 10001)
	payload, err := json.Marshal(map[string]any{"data": entries})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseLemonadeModels(payload); err == nil {
		t.Fatal("unbounded model list accepted")
	}
}
