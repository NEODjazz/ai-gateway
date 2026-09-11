package provider

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/openai"
)

const videoFixture = `{"id":"video_123","object":"video","model":"video-model","status":"queued","progress":0,"created_at":1,"completed_at":null,"expires_at":null,"prompt":"a cat","remixed_from_video_id":null,"seconds":"4","size":"720x1280","error":null}`

func TestOpenAICompatibleVideoLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos":
			if r.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("content-type=%q", r.Header.Get("Content-Type"))
			}
			body, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(body), `"model":"video-model"`) || !strings.Contains(string(body), `"seconds":"4"`) {
				t.Fatalf("body=%s", body)
			}
			_, _ = io.WriteString(w, videoFixture)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos":
			if r.URL.Query().Get("after") != "video_100" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("order") != "asc" {
				t.Fatalf("query=%s", r.URL.RawQuery)
			}
			_, _ = io.WriteString(w, `{"object":"list","data":[`+videoFixture+`],"first_id":"video_123","last_id":"video_123","has_more":false}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_123":
			_, _ = io.WriteString(w, videoFixture)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/videos/video_123/remix":
			_, _ = io.WriteString(w, strings.Replace(videoFixture, `"id":"video_123"`, `"id":"video_456"`, 1))
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/videos/video_123":
			_, _ = io.WriteString(w, `{"id":"video_123","object":"video.deleted","deleted":true}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/videos/video_123/content":
			if r.URL.Query().Get("variant") != "thumbnail" {
				t.Fatalf("query=%s", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "image/webp")
			_, _ = io.WriteString(w, "preview")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewOpenAICompatible(server.URL+"/v1", "secret", false)
	client.client = server.Client()
	created, err := client.CreateVideo(context.Background(), openai.VideoCreateRequest{Model: "video-model", Prompt: "a cat", Seconds: "4", Size: "720x1280"})
	if err != nil || created.ID != "video_123" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	listed, err := client.ListVideos(context.Background(), VideoListOptions{After: "video_100", Limit: 2, Order: "asc"})
	if err != nil || len(listed.Data) != 1 || listed.FirstID != "video_123" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if retrieved, err := client.RetrieveVideo(context.Background(), created.ID); err != nil || retrieved.ID != created.ID {
		t.Fatalf("retrieved=%+v err=%v", retrieved, err)
	}
	if remixed, err := client.RemixVideo(context.Background(), created.ID, openai.VideoRemixRequest{Prompt: "take a bow"}); err != nil || remixed.ID != "video_456" {
		t.Fatalf("remixed=%+v err=%v", remixed, err)
	}
	content, err := client.DownloadVideoContent(context.Background(), created.ID, "thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	defer content.Body.Close()
	payload, _ := io.ReadAll(content.Body)
	if content.ContentType != "image/webp" || string(payload) != "preview" {
		t.Fatalf("content_type=%s payload=%q", content.ContentType, payload)
	}
	if deleted, err := client.DeleteVideo(context.Background(), created.ID); err != nil || !deleted.Deleted {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
}

func TestVideoTransportRejectsInvalidInputsAndResponses(t *testing.T) {
	client := NewOpenAICompatible("http://127.0.0.1:1/v1", "", false)
	for name, call := range map[string]func() error{
		"empty prompt": func() error { _, err := client.CreateVideo(t.Context(), openai.VideoCreateRequest{}); return err },
		"duration": func() error {
			_, err := client.CreateVideo(t.Context(), openai.VideoCreateRequest{Prompt: "x", Seconds: "5"})
			return err
		},
		"reference": func() error {
			_, err := client.CreateVideo(t.Context(), openai.VideoCreateRequest{Prompt: "x", InputReference: &openai.VideoInputReference{FileID: "a", ImageURL: "b"}})
			return err
		},
		"id":         func() error { _, err := client.RetrieveVideo(t.Context(), "bad/id"); return err },
		"variant":    func() error { _, err := client.DownloadVideoContent(t.Context(), "video_1", "source"); return err },
		"pagination": func() error { _, err := client.ListVideos(t.Context(), VideoListOptions{Limit: 101}); return err },
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	t.Run("metadata", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"id":"video_1","object":"video","model":"m","status":"unknown","progress":0,"created_at":1,"seconds":"4","size":"720x1280"}`)
		}))
		defer server.Close()
		invalid := NewOpenAICompatible(server.URL, "", false)
		invalid.client = server.Client()
		if _, err := invalid.RetrieveVideo(t.Context(), "video_1"); err == nil {
			t.Fatal("expected invalid upstream response")
		}
	})

	t.Run("content", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = io.WriteString(w, "not media")
		}))
		defer server.Close()
		invalid := NewOpenAICompatible(server.URL, "", false)
		invalid.client = server.Client()
		_, err := invalid.DownloadVideoContent(t.Context(), "video_1", "")
		if err == nil {
			t.Fatal("expected invalid content type")
		}
	})
}
