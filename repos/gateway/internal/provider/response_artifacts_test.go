package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

type artifactResponseClient struct {
	ownershipResponseClient
	ContainerFileClient
	response     openai.ResponseResponse
	downloads    int
	err          error
	body         io.ReadCloser
	checkContext func() error
}

func (p *artifactResponseClient) RetrieveResponse(context.Context, string) (openai.ResponseResponse, error) {
	p.retrieveCalls++
	return p.response, nil
}

func (p *artifactResponseClient) DownloadContainerFile(ctx context.Context, _, _ string) (ContainerFileContent, error) {
	p.downloads++
	p.checkContext = ctx.Err
	return ContainerFileContent{Body: p.body, ContentType: "text/csv", ContentLength: -1}, p.err
}

func artifactResponse() openai.ResponseResponse {
	return openai.ResponseResponse{ID: "resp_demo", Output: []openai.ResponseOutputItem{{Type: "message", Role: "assistant", Content: []openai.ResponseOutputContent{{Type: "output_text", Annotations: []json.RawMessage{json.RawMessage(`{"type":"container_file_citation","container_id":"cntr_demo","file_id":"cfile_demo","filename":"report.csv"}`)}}}}}}
}

func artifactRouter(t *testing.T) (Router, *artifactResponseClient, modules.RequestContext) {
	t.Helper()
	client := &artifactResponseClient{response: artifactResponse(), body: io.NopCloser(strings.NewReader("rows,1\n"))}
	endpoint := Endpoint{Name: "deployment", Type: "openai-compatible", BaseURL: "https://provider.example.test", CredentialID: "provider-key", Models: []string{"public-model"}, Capabilities: []string{"responses", "container_files"}, Provider: client, RequestTimeout: time.Minute, Admission: newAdmissionController(1, 0, 0)}
	backend := &ownershipTestStore{data: map[string][]byte{}}
	router := Router{endpoints: []Endpoint{endpoint}, health: newEndpointHealthTracker(), ownership: newResponseOwnershipStore(time.Hour, backend)}
	owner := modules.RequestContext{CredentialID: "credential", OrganizationID: "org", UserID: "user"}
	err := router.ownership.put(t.Context(), owner, "resp_demo", responseOwnership{Endpoint: endpoint.Name, Model: "public-model", Resource: "response", Deployment: responseDeploymentIdentity(endpoint)})
	if err != nil {
		t.Fatal(err)
	}
	return router, client, owner
}

func TestResponseArtifactRequiresOwnedCitedFile(t *testing.T) {
	for _, name := range []string{"credential", "user", "organization", "missing response", "missing citation", "other file", "other container", "mismatching response", "tool input", "reasoning", "user message", "invalid ID", "deleted ownership", "changed deployment", "missing capability", "unavailable storage"} {
		t.Run(name, func(t *testing.T) {
			router, client, owner := artifactRouter(t)
			responseID, containerID, fileID := "resp_demo", "cntr_demo", "cfile_demo"
			want := ErrResponseNotFound
			switch name {
			case "credential":
				owner.CredentialID = "other"
			case "user":
				owner.UserID = "other"
			case "organization":
				owner.OrganizationID = "other"
			case "missing response":
				responseID = "resp_unknown"
			case "missing citation":
				client.response.Output = nil
			case "other file":
				fileID = "cfile_other"
			case "other container":
				containerID = "cntr_other"
			case "mismatching response":
				client.response.ID = "resp_other"
			case "tool input":
				client.response.Output[0].Type = "function_call"
			case "reasoning":
				client.response.Output[0].Content[0].Type = "reasoning"
			case "user message":
				client.response.Output[0].Role = "user"
			case "invalid ID":
				fileID = "../other"
			case "deleted ownership":
				clear(router.ownership.store.(*ownershipTestStore).data)
			case "changed deployment":
				router.endpoints[0].CredentialID = "replacement"
				want = ErrResponseDeploymentChanged
			case "missing capability":
				router.endpoints[0].Capabilities = []string{"responses"}
				want = ErrResponseDeploymentChanged
			case "unavailable storage":
				router.ownership.store.(*ownershipTestStore).err = errors.New("unavailable")
				want = ErrResponseOwnershipUnavailable
			}
			_, err := router.DownloadResponseContainerFile(t.Context(), owner, responseID, containerID, fileID)
			if !errors.Is(err, want) || client.downloads != 0 {
				t.Fatalf("err=%v downloads=%d", err, client.downloads)
			}
		})
	}
}

func TestResponseArtifactHoldsAdmissionAndTimeoutThroughBody(t *testing.T) {
	for _, consume := range []bool{false, true} {
		t.Run(map[bool]string{false: "early close", true: "read to EOF"}[consume], func(t *testing.T) {
			router, client, owner := artifactRouter(t)
			content, err := router.DownloadResponseContainerFile(t.Context(), owner, "resp_demo", "cntr_demo", "cfile_demo")
			if err != nil {
				t.Fatal(err)
			}
			if client.checkContext() != nil {
				t.Fatal("download context cancelled at headers")
			}
			if release, err := router.endpoints[0].Admission.acquire(t.Context(), "deployment"); err == nil {
				release()
				t.Fatal("body did not retain admission slot")
			}
			if consume {
				payload, err := io.ReadAll(content.Body)
				if err != nil || string(payload) != "rows,1\n" {
					t.Fatalf("payload=%q err=%v", payload, err)
				}
			}
			if err := content.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if err := content.Body.Close(); err != nil {
				t.Fatal(err)
			}
			if !errors.Is(client.checkContext(), context.Canceled) {
				t.Fatal("download context not released")
			}
			release, err := router.endpoints[0].Admission.acquire(t.Context(), "deployment")
			if err != nil {
				t.Fatal("admission slot leaked", err)
			}
			release()
		})
	}
}

func TestResponseArtifactFailureAndCancellationReleaseAdmission(t *testing.T) {
	for _, name := range []string{"headers", "nil body", "body read", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			router, client, owner := artifactRouter(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch name {
			case "headers":
				client.err = errors.New("headers failed")
			case "nil body":
				client.body = nil
			case "body read":
				client.body = &artifactErrorBody{}
			}
			content, err := router.DownloadResponseContainerFile(ctx, owner, "resp_demo", "cntr_demo", "cfile_demo")
			if name == "headers" || name == "nil body" {
				if err == nil {
					t.Fatal("invalid download accepted")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if name == "cancelled" {
					cancel()
					if client.checkContext() == nil {
						t.Fatal("caller cancellation not propagated")
					}
				}
				if name == "body read" {
					if _, err := io.ReadAll(content.Body); err == nil {
						t.Fatal("body failure ignored")
					}
				}
				if err := content.Body.Close(); err != nil {
					t.Fatal(err)
				}
			}
			release, err := router.endpoints[0].Admission.acquire(t.Context(), "deployment")
			if err != nil {
				t.Fatal("admission leaked", err)
			}
			release()
		})
	}
}

type artifactErrorBody struct{}

func (*artifactErrorBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (*artifactErrorBody) Close() error             { return nil }

func TestResponseArtifactCitationValidation(t *testing.T) {
	for _, raw := range []string{`broken`, `{"type":"url_citation","container_id":"cntr_demo","file_id":"cfile_demo"}`, `{"type":"container_file_citation","container_id":"cntr_demo"}`, strings.Repeat(" ", 64<<10) + `{}`} {
		response := artifactResponse()
		response.Output[0].Content[0].Annotations[0] = json.RawMessage(raw)
		if responseCitesContainerFile(response, "cntr_demo", "cfile_demo") {
			t.Fatal("invalid citation accepted")
		}
	}
	response := artifactResponse()
	citation := response.Output[0].Content[0].Annotations[0]
	response.Output[0].Content[0].Annotations = make([]json.RawMessage, 513)
	response.Output[0].Content[0].Annotations[512] = citation
	if responseCitesContainerFile(response, "cntr_demo", "cfile_demo") {
		t.Fatal("citation scan exceeded bound")
	}
}

func TestResponseArtifactHTTPTransportUsesBoundDeploymentAndKeepsBodyLive(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("local test port unavailable: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var retrieves, downloads atomic.Int64
	allowBody := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-provider-key" {
			t.Error("original deployment authentication missing")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/responses/resp_demo":
			retrieves.Add(1)
			w.Header().Set("Content-Type", "application/json")
			response := artifactResponse()
			response.Model = "upstream-model"
			response.Object = "response"
			response.Status = "completed"
			if err := json.NewEncoder(w).Encode(response); err != nil {
				t.Error(err)
			}
		case "/v1/containers/cntr_demo/files/cfile_demo/content":
			downloads.Add(1)
			w.Header().Set("Content-Type", "text/csv")
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
			select {
			case <-allowBody:
				if _, err := io.WriteString(w, "id,value\n1,exact\n"); err != nil {
					t.Error(err)
				}
			case <-r.Context().Done():
			}
		default:
			t.Error("unexpected provider request path")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	router, _, owner := artifactRouter(t)
	router.endpoints[0].BaseURL = server.URL + "/v1"
	router.endpoints[0].Provider = NewOpenAICompatible(server.URL+"/v1", "test-provider-key", false)
	clear(router.ownership.store.(*ownershipTestStore).data)
	endpoint := router.endpoints[0]
	if err := router.ownership.put(t.Context(), owner, "resp_demo", responseOwnership{Endpoint: endpoint.Name, Model: "public-model", Resource: "response", Deployment: responseDeploymentIdentity(endpoint)}); err != nil {
		t.Fatal(err)
	}
	content, err := router.DownloadResponseContainerFile(t.Context(), owner, "resp_demo", "cntr_demo", "cfile_demo")
	close(allowBody)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := content.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	payload, err := io.ReadAll(content.Body)
	if err != nil || string(payload) != "id,value\n1,exact\n" || retrieves.Load() != 1 || downloads.Load() != 1 {
		t.Fatalf("payload=%q retrieves=%d downloads=%d err=%v", payload, retrieves.Load(), downloads.Load(), err)
	}
}
