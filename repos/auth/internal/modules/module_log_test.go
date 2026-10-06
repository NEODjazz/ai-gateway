package modules

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
)

type failingPipelineModule struct {
	required bool
	err      error
}

func (m failingPipelineModule) Name() string                                  { return "test" }
func (m failingPipelineModule) Required() bool                                { return m.required }
func (m failingPipelineModule) Handle(context.Context, *RequestContext) error { return m.err }

func TestOptionalPipelineRedactsModuleError(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	privateError := errors.New("private prompt and credential")
	if err := NewPipeline([]Module{failingPipelineModule{err: privateError}}).Run(context.Background(), &RequestContext{}); err != nil {
		t.Fatalf("optional module blocked request: %v", err)
	}
	if message := output.String(); !strings.Contains(message, "optional module test skipped") || strings.Contains(message, privateError.Error()) {
		t.Fatalf("optional module log was unsafe: %q", message)
	}
	if err := NewPipeline([]Module{failingPipelineModule{required: true, err: privateError}}).Run(context.Background(), &RequestContext{}); !errors.Is(err, privateError) {
		t.Fatalf("required module error was not preserved: %v", err)
	}
}
