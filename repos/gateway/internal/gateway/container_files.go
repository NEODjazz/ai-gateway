package gateway

import (
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"ai-gateway-gateway/internal/containerstate"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
)

const maxGatewayContainerFileBytes int64 = 32 << 20
const maxGatewayContainerFileContentBytes int64 = 512 << 20

func (h Handler) CreateContainerFile(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return
	}
	owner, runtime, record, ok := h.containerFileOwner(w, r)
	if !ok {
		return
	}
	upload, ok := h.decodeContainerFileUpload(w, r, owner)
	if !ok {
		return
	}
	file, err := runtime.CreateContainerFile(r.Context(), record.Binding, record.Container.ID, upload)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (h Handler) ListContainerFiles(w http.ResponseWriter, r *http.Request) {
	_, runtime, record, ok := h.containerFileOwner(w, r)
	if !ok {
		return
	}
	options, ok := containerFileListOptions(w, r)
	if !ok {
		return
	}
	files, err := runtime.ListContainerFiles(r.Context(), record.Binding, record.Container.ID, options)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (h Handler) GetContainerFile(w http.ResponseWriter, r *http.Request) {
	_, runtime, record, fileID, ok := h.containerFileRecord(w, r)
	if !ok {
		return
	}
	file, err := runtime.RetrieveContainerFile(r.Context(), record.Binding, record.Container.ID, fileID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, file)
}

func (h Handler) DeleteContainerFile(w http.ResponseWriter, r *http.Request) {
	_, runtime, record, fileID, ok := h.containerFileRecord(w, r)
	if !ok {
		return
	}
	deleted, err := runtime.DeleteContainerFile(r.Context(), record.Binding, record.Container.ID, fileID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, deleted)
}

func (h Handler) GetContainerFileContent(w http.ResponseWriter, r *http.Request) {
	_, runtime, record, fileID, ok := h.containerFileRecord(w, r)
	if !ok {
		return
	}
	content, err := runtime.DownloadContainerFile(r.Context(), record.Binding, record.Container.ID, fileID)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	defer content.Body.Close()
	w.Header().Set("Content-Type", content.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if content.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(content.ContentLength, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err = io.Copy(w, &boundedContainerFileReader{reader: content.Body, remaining: maxGatewayContainerFileContentBytes}); err != nil {
		log.Printf("container file content stream failed for %s: %v", fileID, err)
	}
}

func (h Handler) containerFileOwner(w http.ResponseWriter, r *http.Request) (string, provider.ContainerFileProvider, containerstate.Record, bool) {
	owner, _, _, ok := h.containerOwner(w, r)
	if !ok {
		return "", nil, containerstate.Record{}, false
	}
	record, ok := h.containerRecord(w, r, owner)
	if !ok {
		return "", nil, containerstate.Record{}, false
	}
	runtime, ok := h.provider.(provider.ContainerFileProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "container files are not supported")
		return "", nil, containerstate.Record{}, false
	}
	return owner, runtime, record, true
}

func (h Handler) containerFileRecord(w http.ResponseWriter, r *http.Request) (string, provider.ContainerFileProvider, containerstate.Record, string, bool) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "query parameters are not supported")
		return "", nil, containerstate.Record{}, "", false
	}
	owner, runtime, record, ok := h.containerFileOwner(w, r)
	if !ok {
		return "", nil, containerstate.Record{}, "", false
	}
	fileID := r.PathValue("file_id")
	if !validFileToken(fileID, 128) {
		writeError(w, http.StatusBadRequest, "invalid_request", "container file ID is invalid")
		return "", nil, containerstate.Record{}, "", false
	}
	return owner, runtime, record, fileID, true
}

func (h Handler) decodeContainerFileUpload(w http.ResponseWriter, r *http.Request, owner string) (provider.ContainerFileUpload, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "Content-Type must be application/json or multipart/form-data")
		return provider.ContainerFileUpload{}, false
	}
	if mediaType == "application/json" {
		var input openai.ContainerFileCreateRequest
		if !decodeInferenceRequest(w, r, &input) {
			return provider.ContainerFileUpload{}, false
		}
		if !validFileToken(input.FileID, 128) || h.files == nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "owned file_id is required")
			return provider.ContainerFileUpload{}, false
		}
		file, err := h.files.Get(r.Context(), owner, input.FileID, true)
		if err != nil {
			writeFileStoreError(w, err)
			return provider.ContainerFileUpload{}, false
		}
		if file.Bytes > maxGatewayContainerFileBytes || len(file.Content) > int(maxGatewayContainerFileBytes) {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the container upload limit")
			return provider.ContainerFileUpload{}, false
		}
		return provider.ContainerFileUpload{Filename: file.Filename, ContentType: file.ContentType, Content: file.Content}, true
	}
	if mediaType != "multipart/form-data" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Content-Type must be application/json or multipart/form-data")
		return provider.ContainerFileUpload{}, false
	}
	maxBytes := h.fileConfig.MaxBytes
	if maxBytes < 1 || maxBytes > maxGatewayContainerFileBytes {
		maxBytes = maxGatewayContainerFileBytes
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+fileFormOverheadBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid multipart upload")
		return provider.ContainerFileUpload{}, false
	}
	var upload provider.ContainerFileUpload
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || part.FormName() != "file" || upload.Filename != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", "exactly one file part is required")
			return provider.ContainerFileUpload{}, false
		}
		filename := part.FileName()
		if !validContainerFileName(filename) {
			writeError(w, http.StatusBadRequest, "invalid_request", "file name is invalid")
			return provider.ContainerFileUpload{}, false
		}
		content, err := io.ReadAll(io.LimitReader(part, maxBytes+1))
		if err != nil || int64(len(content)) > maxBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "file exceeds the configured limit")
			return provider.ContainerFileUpload{}, false
		}
		upload = provider.ContainerFileUpload{Filename: filename, ContentType: part.Header.Get("Content-Type"), Content: content}
	}
	if upload.Filename == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "exactly one file part is required")
		return provider.ContainerFileUpload{}, false
	}
	return upload, true
}

func validContainerFileName(name string) bool {
	if name == "" || name == "." || name == ".." || name != filepath.Base(name) || strings.ContainsAny(name, "/\\") || len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	for _, character := range name {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func containerFileListOptions(w http.ResponseWriter, r *http.Request) (provider.ContainerFileListOptions, bool) {
	q := r.URL.Query()
	for key, values := range q {
		if (key != "after" && key != "limit" && key != "order") || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "invalid_request", "unsupported or repeated query parameter "+key)
			return provider.ContainerFileListOptions{}, false
		}
	}
	options := provider.ContainerFileListOptions{After: q.Get("after"), Limit: 20, Order: q.Get("order")}
	if options.Order == "" {
		options.Order = "desc"
	}
	if (options.After != "" && !validFileToken(options.After, 128)) || (options.Order != "asc" && options.Order != "desc") {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid container file pagination")
		return options, false
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, http.StatusBadRequest, "invalid_request", "limit must be between 1 and 100")
			return options, false
		}
		options.Limit = limit
	}
	return options, true
}

type boundedContainerFileReader struct {
	reader    io.Reader
	remaining int64
}

func (r *boundedContainerFileReader) Read(buffer []byte) (int, error) {
	if r.remaining < 0 {
		return 0, errors.New("container file content exceeds 512 MiB")
	}
	if int64(len(buffer)) > r.remaining+1 {
		buffer = buffer[:r.remaining+1]
	}
	n, err := r.reader.Read(buffer)
	r.remaining -= int64(n)
	if r.remaining < 0 {
		return n - 1, errors.New("container file content exceeds 512 MiB")
	}
	return n, err
}
