package modules

import (
	"context"
	"testing"
)

type authenticationPipelineModule struct {
	name      string
	establish bool
	calls     int
}

func (m *authenticationPipelineModule) Name() string { return m.name }
func (*authenticationPipelineModule) Required() bool { return true }
func (m *authenticationPipelineModule) Handle(_ context.Context, req *RequestContext) error {
	m.calls++
	if m.establish {
		req.CredentialID = "credential"
	}
	return nil
}

func TestRunAuthenticationOnlyRunsAuth(t *testing.T) {
	auth := &authenticationPipelineModule{name: "auth", establish: true}
	billing := &authenticationPipelineModule{name: "billing"}
	content := &authenticationPipelineModule{name: "dlp"}
	req := RequestContext{}
	if err := NewPipeline([]Module{auth, billing, content}).RunAuthentication(t.Context(), &req); err != nil {
		t.Fatal(err)
	}
	if auth.calls != 1 || billing.calls != 0 || content.calls != 0 || req.CredentialID != "credential" {
		t.Fatalf("auth=%d billing=%d content=%d credential=%q", auth.calls, billing.calls, content.calls, req.CredentialID)
	}
}

func TestRunAuthenticationRequiresEstablishedCredential(t *testing.T) {
	if err := NewPipeline(nil).RunAuthentication(t.Context(), &RequestContext{}); err != ErrUnauthorized {
		t.Fatalf("missing auth err=%v", err)
	}
	auth := &authenticationPipelineModule{name: "auth"}
	req := RequestContext{}
	if err := NewPipeline([]Module{auth}).RunAuthentication(t.Context(), &req); err != ErrUnauthorized {
		t.Fatalf("missing credential err=%v", err)
	}
}
