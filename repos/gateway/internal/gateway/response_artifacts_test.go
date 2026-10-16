package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/provider"
)

type gatewayArtifactProvider struct {
	lifecycleResourceProvider
	content   provider.ContainerFileContent
	err       error
	downloads int
}

func (p *gatewayArtifactProvider) DownloadResponseContainerFile(_ context.Context, req modules.RequestContext, responseID, containerID, fileID string) (provider.ContainerFileContent, error) {
	p.downloads++
	if req.APIKey != "" || req.CredentialID == "" || req.Request.Model != "test-model" || responseID != "resp_demo" || containerID != "cntr_demo" || fileID != "cfile_demo" {
		return provider.ContainerFileContent{}, errors.New("invalid resource scope")
	}
	return p.content, p.err
}

func TestResponseFileContentAuthorizationAndLimits(t *testing.T) {
	for _, name := range []string{"valid", "unauthenticated", "model revoked", "tool revoked", "query", "path", "not found", "deployment changed", "storage unavailable", "known oversized", "unknown oversized", "read failure", "nil body"} {
		t.Run(name, func(t *testing.T) {
			body := &artifactTrackedBody{Reader: strings.NewReader("rows,1\n")}
			resource := &gatewayArtifactProvider{content: provider.ContainerFileContent{Body: body, ContentType: "text/csv", ContentLength: -1}}
			auth := modules.Module(&lifecycleAuthModule{})
			billing := &lifecycleBillingModule{}
			path := "/v1/responses/resp_demo/containers/cntr_demo/files/cfile_demo/content"
			want := http.StatusOK
			switch name {
			case "unauthenticated":
				auth = modules.NewAuthModule(true)
				want = 401
			case "model revoked":
				auth = &lifecycleAuthModule{allowedModels: []string{"other"}}
				want = 403
			case "tool revoked":
				auth = &lifecycleAuthModule{allowedTools: []string{"other"}}
				want = 403
			case "query":
				path += "?token=ignored"
				want = 400
			case "path":
				path = strings.Replace(path, "cfile_demo", "bad%3Fid", 1)
				want = 400
			case "not found":
				resource.err = provider.ErrResponseNotFound
				want = 404
			case "deployment changed":
				resource.err = provider.ErrResponseDeploymentChanged
				want = 409
			case "storage unavailable":
				resource.err = provider.ErrResponseOwnershipUnavailable
				want = 503
			case "known oversized":
				resource.content.ContentLength = maxResponseArtifactBytes + 1
				want = 413
			case "unknown oversized":
				body.Reader = io.LimitReader(artifactZeroReader{}, maxResponseArtifactBytes+1)
				want = 413
			case "read failure":
				body.Reader = artifactFailedReader{}
				want = 502
			case "nil body":
				resource.content.Body = nil
				want = 502
			}
			handler := Routes(NewHandler(modules.NewPipeline([]modules.Module{auth, billing}), resource))
			request := httptest.NewRequest(http.MethodGet, path, nil)
			if name != "unauthenticated" {
				request.Header.Set("Authorization", "Bearer test-key")
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, want, recorder.Body.String())
			}
			if billing.calls != 0 {
				t.Fatal("file read started inference billing")
			}
			if name == "valid" {
				if recorder.Body.String() != "rows,1\n" || recorder.Header().Get("Content-Length") != "7" || recorder.Header().Get("Cache-Control") != "no-store" || recorder.Header().Get("Content-Disposition") != "attachment" || recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Fatal("invalid file response", recorder.Header())
				}
			}
			if name == "unauthenticated" || name == "model revoked" || name == "tool revoked" || name == "query" || name == "path" {
				if resource.downloads != 0 {
					t.Fatal("denied request downloaded file")
				}
			} else if resource.err == nil && resource.content.Body != nil && !body.closed {
				t.Fatal("download body leaked")
			}
		})
	}
}

type artifactTrackedBody struct {
	io.Reader
	closed bool
}

func (b *artifactTrackedBody) Close() error { b.closed = true; return nil }

type artifactZeroReader struct{}

func (artifactZeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type artifactFailedReader struct{}

func (artifactFailedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
