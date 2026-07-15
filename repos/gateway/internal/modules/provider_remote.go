package modules

import (
	"context"
	"errors"
	"strings"
)

type ProviderRemoteModule struct {
	name        string
	required    bool
	metadataKey string
	remote      RemoteModule
}

func NewProviderRemoteModule(name string, required bool, endpoint string) ProviderRemoteModule {
	return ProviderRemoteModule{
		name:        name,
		required:    required,
		metadataKey: "provider.modules." + name + ".enabled",
		remote:      NewRemoteModule(name, required, endpoint),
	}
}

func (m ProviderRemoteModule) Name() string {
	return m.name
}

func (m ProviderRemoteModule) Required() bool {
	return m.required
}

func (m ProviderRemoteModule) Handle(ctx context.Context, req *RequestContext) error {
	if !metadataBool(req.Metadata, m.metadataKey) {
		return nil
	}
	if strings.TrimSpace(m.remote.endpoint) == "" {
		return errors.New("remote module url is empty")
	}
	return m.remote.Handle(ctx, req)
}

func metadataBool(metadata map[string]string, key string) bool {
	switch strings.ToLower(strings.TrimSpace(metadata[key])) {
	case "1", "true", "yes", "on", "enabled":
		return true
	default:
		return false
	}
}
