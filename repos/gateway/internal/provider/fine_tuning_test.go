package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/config"
	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

func TestOpenAICompatibleFineTuningLifecycle(t *testing.T) {
	job := `{"id":"ftjob_123","object":"fine_tuning.job","created_at":1,"finished_at":null,"fine_tuned_model":null,"model":"base-model","organization_id":"org","result_files":[],"status":"queued","trained_tokens":null,"training_file":"file_train","validation_file":null,"error":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer provider-key" || r.Header.Get("Accept") != "application/json" {
			t.Errorf("headers=%v", r.Header)
		}
		switch r.URL.Path {
		case "/v1/fine_tuning/jobs":
			if r.Method == http.MethodPost {
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("content type=%q", r.Header.Get("Content-Type"))
				}
				w.Write([]byte(job))
				return
			}
			if r.URL.Query().Get("after") != "cursor" || r.URL.Query().Get("limit") != "2" {
				t.Errorf("query=%q", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"object":"list","data":[%s],"has_more":false}`, job)
		case "/v1/fine_tuning/jobs/ftjob_123", "/v1/fine_tuning/jobs/ftjob_123/cancel", "/v1/fine_tuning/jobs/ftjob_123/pause", "/v1/fine_tuning/jobs/ftjob_123/resume":
			w.Write([]byte(job))
		case "/v1/fine_tuning/jobs/ftjob_123/events":
			w.Write([]byte(`{"object":"list","data":[{"id":"ftevent_1","object":"fine_tuning.job.event","created_at":1,"level":"info","message":"queued"}],"has_more":false}`))
		case "/v1/fine_tuning/jobs/ftjob_123/checkpoints":
			w.Write([]byte(`{"object":"list","data":[{"id":"ftckpt_1","object":"fine_tuning.job.checkpoint","created_at":1,"fine_tuned_model_checkpoint":"model:step-1","fine_tuning_job_id":"ftjob_123","metrics":{},"step_number":1}],"has_more":false}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewOpenAICompatible(server.URL+"/v1", "provider-key", false)

	created, err := client.CreateFineTuningJob(context.Background(), openai.FineTuningCreateRequest{Model: "base-model", TrainingFile: "file_train"})
	if err != nil || created.ID != "ftjob_123" {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	listed, err := client.ListFineTuningJobs(context.Background(), FineTuningListOptions{After: "cursor", Limit: 2})
	if err != nil || len(listed.Data) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	for name, call := range map[string]func(context.Context, string) (openai.FineTuningJob, error){
		"retrieve": client.RetrieveFineTuningJob,
		"cancel":   client.CancelFineTuningJob,
		"pause":    client.PauseFineTuningJob,
		"resume":   client.ResumeFineTuningJob,
	} {
		t.Run(name, func(t *testing.T) {
			result, callErr := call(context.Background(), "ftjob_123")
			if callErr != nil || result.ID != "ftjob_123" {
				t.Fatalf("result=%+v err=%v", result, callErr)
			}
		})
	}
	events, err := client.ListFineTuningEvents(context.Background(), "ftjob_123", FineTuningListOptions{})
	if err != nil || len(events.Data) != 1 || events.Data[0].Message != "queued" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	checkpoints, err := client.ListFineTuningCheckpoints(context.Background(), "ftjob_123", FineTuningListOptions{})
	if err != nil || len(checkpoints.Data) != 1 || checkpoints.Data[0].StepNumber != 1 {
		t.Fatalf("checkpoints=%+v err=%v", checkpoints, err)
	}
}

func TestFineTuningRouterSelectsCapableDeploymentAndPinsLifecycle(t *testing.T) {
	job := `{"id":"ftjob_route","object":"fine_tuning.job","created_at":1,"finished_at":null,"fine_tuned_model":null,"model":"upstream-model","organization_id":"org","result_files":[],"status":"queued","trained_tokens":null,"training_file":"file_train","validation_file":null,"error":null}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/fine_tuning/jobs" && r.URL.Path != "/v1/fine_tuning/jobs/ftjob_route" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(job))
	}))
	defer server.Close()
	router := New(Config{Endpoints: []config.ProviderEndpointConfig{{Name: "training", Type: "openai", BaseURL: server.URL + "/v1", Models: []string{"public-model"}, ModelAliases: map[string]string{"public-model": "upstream-model"}, Capabilities: []string{"fine_tuning"}}}})
	fineTuning, ok := router.(FineTuningProvider)
	if !ok {
		t.Fatal("router does not expose fine-tuning")
	}
	created, binding, err := fineTuning.CreateFineTuningJob(t.Context(), modules.RequestContext{}, openai.FineTuningCreateRequest{Model: "public-model", TrainingFile: "file_train"})
	if err != nil || created.Model != "public-model" || binding.Endpoint != "training" || binding.Model != "public-model" || len(binding.Deployment) != 64 {
		t.Fatalf("created=%+v binding=%+v err=%v", created, binding, err)
	}
	retrieved, err := fineTuning.RetrieveFineTuningJob(t.Context(), binding, created.ID)
	if err != nil || retrieved.ID != created.ID {
		t.Fatalf("retrieved=%+v err=%v", retrieved, err)
	}
	binding.Deployment = strings.Repeat("0", 64)
	if _, err := fineTuning.RetrieveFineTuningJob(t.Context(), binding, created.ID); !errors.Is(err, ErrFineTuningDeploymentChanged) {
		t.Fatalf("changed deployment error=%v", err)
	}
}

func TestFineTuningTransportRejectsInvalidInputsAndResponses(t *testing.T) {
	client := NewOpenAICompatible("http://unused.invalid/v1", "", false)
	if _, err := client.RetrieveFineTuningJob(context.Background(), "bad/id"); err == nil {
		t.Fatal("invalid job ID accepted")
	}
	if _, err := client.ListFineTuningJobs(context.Background(), FineTuningListOptions{Limit: 101}); err == nil {
		t.Fatal("invalid pagination accepted")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"object":"fine_tuning.job","id":"ftjob_1","model":"m","training_file":"f","status":"mystery"}`))
	}))
	defer server.Close()
	invalid := NewOpenAICompatible(server.URL+"/v1", "", false)
	if _, err := invalid.RetrieveFineTuningJob(context.Background(), "ftjob_1"); err == nil || !strings.Contains(err.Error(), "invalid upstream") {
		t.Fatalf("invalid upstream error=%v", err)
	}
}
