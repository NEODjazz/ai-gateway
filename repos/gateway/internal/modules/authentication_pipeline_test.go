package modules

import (
	"context"
	"strings"
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

func TestRunTokenCountAfterAuthenticationSkipsAuthAndBilling(t *testing.T) {
	auth := &authenticationPipelineModule{name: "auth", establish: true}
	billing := &authenticationPipelineModule{name: "billing"}
	content := &authenticationPipelineModule{name: "dlp"}
	req := RequestContext{CredentialID: "credential"}
	if err := NewPipeline([]Module{auth, billing, content}).RunTokenCountAfterAuthentication(t.Context(), &req); err != nil {
		t.Fatal(err)
	}
	if auth.calls != 0 || billing.calls != 0 || content.calls != 1 {
		t.Fatalf("auth=%d billing=%d content=%d", auth.calls, billing.calls, content.calls)
	}
}

func TestRunBillingLifecycleOnlyRunsBillingPhase(t *testing.T) {
	auth := &authenticationPipelineModule{name: "auth", establish: true}
	billing := &billingLifecyclePipelineModule{}
	content := &authenticationPipelineModule{name: "dlp"}
	pipeline := NewPipeline([]Module{auth, billing, content})
	req := RequestContext{}
	if err := pipeline.RunBillingLifecycle(t.Context(), &req, "reserve", nil); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.RunBillingLifecycle(t.Context(), &req, "commit", nil); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.RunBillingLifecycle(t.Context(), &req, "cancel", context.Canceled); err != nil {
		t.Fatal(err)
	}
	if strings.Join(billing.phases, ",") != "reserve,commit,cancel" || auth.calls != 0 || content.calls != 0 {
		t.Fatalf("phases=%v auth=%d content=%d", billing.phases, auth.calls, content.calls)
	}
}

type billingLifecyclePipelineModule struct{ phases []string }

func (*billingLifecyclePipelineModule) Name() string              { return "billing" }
func (*billingLifecyclePipelineModule) Required() bool            { return true }
func (*billingLifecyclePipelineModule) PostResponseEnabled() bool { return true }
func (m *billingLifecyclePipelineModule) Handle(context.Context, *RequestContext) error {
	m.phases = append(m.phases, "reserve")
	return nil
}
func (m *billingLifecyclePipelineModule) HandlePostResponse(context.Context, *RequestContext) error {
	m.phases = append(m.phases, "commit")
	return nil
}
func (m *billingLifecyclePipelineModule) HandleFailure(context.Context, *RequestContext, error) error {
	m.phases = append(m.phases, "cancel")
	return nil
}
