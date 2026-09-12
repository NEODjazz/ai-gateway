package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

const maxContainerFileContentBytes = 512 << 20

type ContainerFileUpload struct {
	Filename    string
	ContentType string
	Content     []byte
}

type ContainerFileListOptions struct {
	After string
	Limit int
	Order string
}

type ContainerFileContent struct {
	Body          io.ReadCloser
	ContentType   string
	ContentLength int64
}

type ContainerFileClient interface {
	CreateContainerFile(context.Context, string, ContainerFileUpload) (openai.ContainerFile, error)
	ListContainerFiles(context.Context, string, ContainerFileListOptions) (openai.ContainerFileList, error)
	RetrieveContainerFile(context.Context, string, string) (openai.ContainerFile, error)
	DeleteContainerFile(context.Context, string, string) (openai.ContainerDeletion, error)
	DownloadContainerFile(context.Context, string, string) (ContainerFileContent, error)
}

func (p OpenAICompatible) CreateContainerFile(ctx context.Context, containerID string, upload ContainerFileUpload) (openai.ContainerFile, error) {
	var result openai.ContainerFile
	if !validResponseResourceID(containerID) || !validContainerFilename(upload.Filename) || !validContainerContentType(upload.ContentType) || len(upload.Content) > 32<<20 {
		return result, errors.New("invalid container file upload")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": upload.Filename}))
	if upload.ContentType != "" {
		header.Set("Content-Type", upload.ContentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		return result, err
	}
	if _, err = part.Write(upload.Content); err != nil {
		return result, err
	}
	if err = writer.Close(); err != nil {
		return result, err
	}
	request, err := p.containerRequest(ctx, http.MethodPost, "containers/"+containerID+"/files", &body, writer.FormDataContentType())
	if err != nil {
		return result, err
	}
	err = p.doContainerJSON(request, &result)
	if err == nil {
		err = validateContainerFile(result, containerID)
	}
	return result, err
}

func (p OpenAICompatible) ListContainerFiles(ctx context.Context, containerID string, options ContainerFileListOptions) (openai.ContainerFileList, error) {
	var result openai.ContainerFileList
	if !validResponseResourceID(containerID) || options.Limit < 1 || options.Limit > 100 || options.After != "" && !validResponseResourceID(options.After) || options.Order != "asc" && options.Order != "desc" {
		return result, errors.New("invalid container file list options")
	}
	query := url.Values{"limit": []string{strconv.Itoa(options.Limit)}, "order": []string{options.Order}}
	if options.After != "" {
		query.Set("after", options.After)
	}
	request, err := p.containerRequest(ctx, http.MethodGet, "containers/"+containerID+"/files?"+query.Encode(), http.NoBody, "")
	if err != nil {
		return result, err
	}
	err = p.doContainerJSON(request, &result)
	if err != nil {
		return result, err
	}
	if result.Object != "list" || len(result.Data) > 100 || result.FirstID != "" && !validResponseResourceID(result.FirstID) || result.LastID != "" && !validResponseResourceID(result.LastID) {
		return result, errors.New("invalid upstream container file list")
	}
	for _, file := range result.Data {
		if err = validateContainerFile(file, containerID); err != nil {
			return openai.ContainerFileList{}, err
		}
	}
	return result, nil
}

func (p OpenAICompatible) RetrieveContainerFile(ctx context.Context, containerID, fileID string) (openai.ContainerFile, error) {
	var result openai.ContainerFile
	if !validResponseResourceID(containerID) || !validResponseResourceID(fileID) {
		return result, errors.New("invalid container file ID")
	}
	request, err := p.containerRequest(ctx, http.MethodGet, "containers/"+containerID+"/files/"+fileID, http.NoBody, "")
	if err != nil {
		return result, err
	}
	err = p.doContainerJSON(request, &result)
	if err == nil {
		err = validateContainerFile(result, containerID)
	}
	return result, err
}

func (p OpenAICompatible) DeleteContainerFile(ctx context.Context, containerID, fileID string) (openai.ContainerDeletion, error) {
	var result openai.ContainerDeletion
	if !validResponseResourceID(containerID) || !validResponseResourceID(fileID) {
		return result, errors.New("invalid container file ID")
	}
	request, err := p.containerRequest(ctx, http.MethodDelete, "containers/"+containerID+"/files/"+fileID, http.NoBody, "")
	if err != nil {
		return result, err
	}
	err = p.doContainerJSON(request, &result)
	if err == nil && (result.ID != fileID || result.Object != "container.file.deleted" || !result.Deleted) {
		err = errors.New("invalid upstream container file deletion")
	}
	return result, err
}

func (p OpenAICompatible) DownloadContainerFile(ctx context.Context, containerID, fileID string) (ContainerFileContent, error) {
	if !validResponseResourceID(containerID) || !validResponseResourceID(fileID) {
		return ContainerFileContent{}, errors.New("invalid container file ID")
	}
	request, err := p.containerRequest(ctx, http.MethodGet, "containers/"+containerID+"/files/"+fileID+"/content", http.NoBody, "")
	if err != nil {
		return ContainerFileContent{}, err
	}
	request.Header.Set("Accept", "*/*")
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return ContainerFileContent{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		return ContainerFileContent{}, responseStatusError(p.providerName(), response)
	}
	if response.ContentLength > maxContainerFileContentBytes {
		response.Body.Close()
		return ContainerFileContent{}, errors.New("upstream container file exceeds 512 MiB")
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return ContainerFileContent{Body: response.Body, ContentType: contentType, ContentLength: response.ContentLength}, nil
}

func validateContainerFile(file openai.ContainerFile, containerID string) error {
	if !validResponseResourceID(file.ID) || file.Object != "container.file" || file.ContainerID != containerID || file.CreatedAt < 0 || file.Bytes < 0 || file.Bytes > maxContainerFileContentBytes || file.Path == "" || len(file.Path) > 4096 || file.Source == "" || len(file.Source) > 64 {
		return errors.New("invalid upstream container file response")
	}
	return nil
}

func validContainerFilename(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsAny(name, "/\\") {
		return false
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validContainerContentType(value string) bool {
	if value == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType != "" && len(value) <= 255
}

func (p OpenAICompatible) containerRequest(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, providerURL(p.baseURL, path), body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	if p.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	return request, nil
}

func (p OpenAICompatible) doContainerJSON(request *http.Request, output any) error {
	client := *p.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseStatusError(p.providerName(), response)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxContainerMetadataBytes+1))
	if err != nil {
		return err
	}
	if len(payload) > maxContainerMetadataBytes {
		return errors.New("upstream container response exceeds 1 MiB")
	}
	if json.Unmarshal(payload, output) != nil {
		return errors.New("invalid upstream container response")
	}
	return nil
}
