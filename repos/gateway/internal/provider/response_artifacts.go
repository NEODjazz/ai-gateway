package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

// DownloadResponseContainerFile never infers container ownership from a client
// supplied ID. The original deployment must return a matching output citation.
func (r Router) DownloadResponseContainerFile(ctx context.Context, req modules.RequestContext, responseID, containerID, fileID string) (ContainerFileContent, error) {
	if !validResponseResourceID(containerID) || !validResponseResourceID(fileID) {
		return ContainerFileContent{}, ErrResponseNotFound
	}
	_, endpoint, err := r.responseResource(ctx, req, responseID)
	if err != nil {
		return ContainerFileContent{}, err
	}
	retriever, canRetrieve := endpoint.Provider.(responseRetrieveClient)
	downloader, canDownload := endpoint.Provider.(interface {
		DownloadContainerFile(context.Context, string, string) (ContainerFileContent, error)
	})
	if !canRetrieve || !canDownload || !endpoint.supportsCapabilities("container_files") {
		return ContainerFileContent{}, ErrResponseDeploymentChanged
	}
	response, err := callResponseLifecycle(r, ctx, endpoint, "responses.retrieve", func(callCtx context.Context) (openai.ResponseResponse, error) {
		return retriever.RetrieveResponse(callCtx, responseID)
	})
	if err != nil {
		return ContainerFileContent{}, err
	}
	if response.ID != responseID || !responseCitesContainerFile(response, containerID, fileID) {
		return ContainerFileContent{}, ErrResponseNotFound
	}
	release, err := r.acquireEndpoint(ctx, endpoint, 0)
	if err != nil {
		return ContainerFileContent{}, err
	}
	if err = r.health.permit(ctx, endpoint); err != nil {
		release()
		return ContainerFileContent{}, err
	}
	providerCtx, finish := r.startProviderCall(ctx, endpoint, "responses.file.content")
	content, err := downloader.DownloadContainerFile(providerCtx, containerID, fileID)
	if err == nil && content.Body == nil {
		err = errors.New("provider returned no response file content")
	}
	if err != nil {
		if content.Body != nil {
			_ = content.Body.Close()
		}
		finish(err)
		release()
		r.health.failure(ctx, endpoint, err)
		return ContainerFileContent{}, err
	}
	// Keep the provider timeout and admission slot until the body is consumed or
	// closed. Finishing at HTTP headers would cancel an in-progress download.
	content.Body = &responseArtifactBody{ReadCloser: content.Body, finish: func(err error) {
		finish(err)
		release()
		if err != nil {
			r.health.failure(context.WithoutCancel(ctx), endpoint, err)
		} else {
			r.health.success(context.WithoutCancel(ctx), endpoint)
		}
	}}
	return content, nil
}

func responseCitesContainerFile(response openai.ResponseResponse, containerID, fileID string) bool {
	visited := 0
	for _, item := range response.Output {
		if item.Type != "message" || item.Role != "assistant" {
			continue
		}
		for _, content := range item.Content {
			if content.Type != "output_text" {
				continue
			}
			for _, raw := range content.Annotations {
				visited++
				if visited > 512 {
					return false
				}
				var citation struct {
					Type        string `json:"type"`
					ContainerID string `json:"container_id"`
					FileID      string `json:"file_id"`
				}
				if len(raw) <= 64<<10 && json.Unmarshal(raw, &citation) == nil && citation.Type == "container_file_citation" && citation.ContainerID == containerID && citation.FileID == fileID {
					return true
				}
			}
		}
	}
	return false
}

type responseArtifactBody struct {
	io.ReadCloser
	once   sync.Once
	finish func(error)
}

func (b *responseArtifactBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		result := err
		if errors.Is(err, io.EOF) {
			result = nil
		}
		b.once.Do(func() { b.finish(result) })
	}
	return n, err
}

func (b *responseArtifactBody) Close() error {
	err := b.ReadCloser.Close()
	b.once.Do(func() { b.finish(err) })
	return err
}
