package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type lemonadeMediaAccounting struct{ images, audioMilliseconds int }

func (*lemonadeMediaAccounting) Name() string                                          { return "lemonade-media-accounting" }
func (*lemonadeMediaAccounting) Required() bool                                        { return true }
func (*lemonadeMediaAccounting) PostResponseEnabled() bool                             { return true }
func (*lemonadeMediaAccounting) Handle(context.Context, *modules.RequestContext) error { return nil }
func (m *lemonadeMediaAccounting) HandlePostResponse(_ context.Context, req *modules.RequestContext) error {
	if response := req.ImageGenerationResponse; response != nil {
		m.images = len(response.Data)
	}
	if response := req.AudioTranscriptionResponse; response != nil && response.Usage != nil {
		m.audioMilliseconds = response.Usage.InputAudioMilliseconds
	}
	return nil
}

func TestLemonadeRouterMediaAccounting(t *testing.T) {
	for _, operation := range []string{"image_generation", "image_edit", "image_variation", "audio_transcription"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if operation == "audio_transcription" {
					fmt.Fprint(w, `{"text":"hello"}`)
				} else {
					fmt.Fprint(w, `{"created":1,"data":[{"b64_json":"iVBORw0KGgpmaXh0dXJl"}]}`)
				}
			}))
			t.Cleanup(server.Close)
			accounting := &lemonadeMediaAccounting{}
			router := New(Config{Modules: modules.NewPipeline([]modules.Module{accounting}), Endpoints: []config.ProviderEndpointConfig{{Name: "local", Type: "lemonade", BaseURL: server.URL, Models: []string{"native"}, Capabilities: []string{operation}}}}).(*Router)
			req := modules.RequestContext{Request: openai.ChatCompletionRequest{Provider: "local", Model: "native"}}
			var err error
			switch operation {
			case "image_generation":
				req.ImageGenerationRequest = &openai.ImageGenerationRequest{Model: "native", Prompt: "draw"}
				_, err = router.GenerateImage(t.Context(), req)
			case "image_edit":
				req.ImageEditRequest = &openai.ImageEditRequest{Model: "native", Prompt: "edit", Images: []openai.ImageAttachment{editAttachment()}}
				_, err = router.EditImage(t.Context(), req)
			case "image_variation":
				req.ImageVariationRequest = &openai.ImageVariationRequest{Model: "native", Image: editAttachment()}
				_, err = router.CreateImageVariation(t.Context(), req)
			case "audio_transcription":
				req.AudioTranscriptionRequest = &openai.AudioTranscriptionRequest{Model: "native", File: mistralWAVAttachment(1500)}
				_, err = router.TranscribeAudio(t.Context(), req)
			}
			if err != nil {
				t.Fatal(err)
			}
			if operation == "audio_transcription" {
				if accounting.audioMilliseconds != 1500 {
					t.Fatalf("measured duration lost: %d", accounting.audioMilliseconds)
				}
			} else if accounting.images != 1 {
				t.Fatalf("returned image count lost: %d", accounting.images)
			}
		})
	}
}

func TestLemonadeImageAndAudioContracts(t *testing.T) {
	for _, operation := range []string{"images/generations", "images/edits", "images/variations", "audio/transcriptions", "audio/speech"} {
		t.Run(operation, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/proxy/v1/"+operation || r.Header.Get("Authorization") != "Bearer test-token" {
					t.Error("incorrect native media route or authentication")
				}
				if operation == "images/edits" || operation == "images/variations" || operation == "audio/transcriptions" {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
						http.Error(w, "invalid multipart", 400)
						return
					}
					defer func() {
						if err := r.MultipartForm.RemoveAll(); err != nil {
							t.Error(err)
						}
					}()
					part := "image"
					if operation == "audio/transcriptions" {
						part = "file"
					}
					if r.FormValue("model") != "native" || len(r.MultipartForm.File[part]) != 1 {
						t.Error("model or attachment missing")
					}
					if operation != "audio/transcriptions" && r.FormValue("response_format") != "b64_json" {
						t.Error("incorrect image response format")
					}
				} else {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "native" {
						t.Error("invalid JSON media request")
					}
					if operation == "images/generations" && body["response_format"] != "b64_json" {
						t.Error("incorrect image response format")
					}
				}
				if operation == "audio/speech" {
					w.Header().Set("Content-Type", "audio/wav")
					fmt.Fprint(w, "RIFF....WAVEdata")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if operation == "audio/transcriptions" {
					fmt.Fprint(w, `{"text":"hello"}`)
					return
				}
				fmt.Fprint(w, `{"created":1,"data":[{"b64_json":"iVBORw0KGgpmaXh0dXJl"}]}`)
			}))
			t.Cleanup(server.Close)
			client := NewLemonade(server.URL+"/proxy/v1", "test-token", true)
			switch operation {
			case "images/generations":
				response, err := client.GenerateImage(t.Context(), openai.ImageGenerationRequest{Model: "native", Prompt: "draw"})
				if err != nil || len(response.Data) != 1 {
					t.Fatalf("image=%+v err=%v", response, err)
				}
			case "images/edits":
				response, err := client.EditImage(t.Context(), openai.ImageEditRequest{Model: "native", Prompt: "edit", Images: []openai.ImageAttachment{editAttachment()}})
				if err != nil || len(response.Data) != 1 {
					t.Fatalf("edit=%+v err=%v", response, err)
				}
			case "images/variations":
				response, err := client.CreateImageVariation(t.Context(), openai.ImageVariationRequest{Model: "native", Image: editAttachment()})
				if err != nil || len(response.Data) != 1 {
					t.Fatalf("variation=%+v err=%v", response, err)
				}
			case "audio/transcriptions":
				response, err := client.TranscribeAudio(t.Context(), openai.AudioTranscriptionRequest{Model: "native", File: mistralWAVAttachment(1500)})
				if err != nil || response.Text != "hello" || response.Usage == nil || response.Usage.Type != "duration" || response.Usage.InputAudioMilliseconds != 1500 {
					t.Fatalf("transcription=%+v err=%v", response, err)
				}
			case "audio/speech":
				response, err := client.GenerateSpeech(t.Context(), openai.AudioSpeechRequest{Model: "native", Input: "hello", Voice: "af_heart", ResponseFormat: "wav"})
				if err != nil || string(response.Data) != "RIFF....WAVEdata" || response.ContentType != "audio/wav" {
					t.Fatalf("speech=%+v err=%v", response, err)
				}
			}
		})
	}
}
