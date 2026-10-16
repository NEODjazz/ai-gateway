// Package documentprocessing implements the internal Docling task contract.
// Task IDs and results remain private to the authenticated inference request.
package documentprocessing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ai-gateway-gateway/internal/openai"
)

var (
	ErrUnavailable = errors.New("document processing unavailable")
	ErrBusy        = errors.New("document processing queue is full")
	ErrFailed      = errors.New("document conversion failed or incomplete")
	ErrTooLarge    = errors.New("converted document exceeds text limit")
)

type Converter interface {
	Convert(context.Context, openai.ResponseFileAttachment, string) (string, error)
}

type Config struct {
	URL          string
	APIKey       string
	Timeout      time.Duration
	PollInterval time.Duration
	MaxTextBytes int
}

type Client struct {
	config Config
	http   *http.Client
}

func New(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || strings.TrimSpace(cfg.APIKey) == "" {
		return nil, ErrUnavailable
	}
	if cfg.Timeout <= 0 || cfg.Timeout > 10*time.Minute || cfg.PollInterval <= 0 || cfg.MaxTextBytes <= 0 || cfg.MaxTextBytes > 16<<20 {
		return nil, ErrUnavailable
	}
	cfg.URL = strings.TrimRight(cfg.URL, "/")
	return &Client{config: cfg, http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) Convert(ctx context.Context, file openai.ResponseFileAttachment, owner string) (string, error) {
	if len(owner) != 64 {
		return "", ErrUnavailable
	}
	ctx = context.WithValue(ctx, ownerContextKey{}, owner)
	ctx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	// Conversion options are server-owned. No external URL, callback, image
	// export, custom model or remote OCR configuration comes from the client.
	body := map[string]any{
		"sources": []any{map[string]any{"kind": "file", "filename": "document.pdf", "base64_string": file.Data}},
		"options": map[string]any{"to_formats": []string{"md"}, "image_export_mode": "placeholder", "do_ocr": true, "abort_on_error": true},
	}
	var task struct {
		ID     string `json:"task_id"`
		Status string `json:"task_status"`
	}
	if err := c.call(ctx, http.MethodPost, "/v1/convert/source/async", body, &task, 64<<10); err != nil {
		return "", err
	}
	if !validTaskID(task.ID) {
		return "", ErrFailed
	}
	for {
		switch task.Status {
		case "success":
			var result struct {
				Status   string `json:"status"`
				Document struct {
					Markdown string `json:"md_content"`
				} `json:"document"`
				Errors []json.RawMessage `json:"errors"`
			}
			// JSON escaping can expand UTF-8 text; the separate decoded limit is
			// authoritative. No truncation is ever returned as a valid document.
			if err := c.call(ctx, http.MethodGet, "/v1/result/"+task.ID, nil, &result, 6*c.config.MaxTextBytes+64<<10); err != nil {
				return "", err
			}
			text := result.Document.Markdown
			if len(text) > c.config.MaxTextBytes {
				return "", ErrTooLarge
			}
			if result.Status != "success" || len(result.Errors) != 0 || strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
				return "", ErrFailed
			}
			return text, nil
		case "failure":
			return "", ErrFailed
		case "pending", "started":
		default:
			return "", ErrFailed
		}
		timer := time.NewTimer(c.config.PollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
		var status struct {
			ID     string `json:"task_id"`
			Status string `json:"task_status"`
		}
		if err := c.call(ctx, http.MethodGet, "/v1/status/poll/"+task.ID, nil, &status, 64<<10); err != nil {
			return "", err
		}
		if status.ID != task.ID {
			return "", ErrFailed
		}
		task.Status = status.Status
	}
}

func validTaskID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, ch := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if ch != '-' {
				return false
			}
			continue
		}
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func (c *Client) call(ctx context.Context, method, path string, body any, result any, limit int) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return ErrFailed
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.config.URL+path, bytes.NewReader(encoded))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", c.config.APIKey)
	if owner, ok := ctx.Value(ownerContextKey{}).(string); ok {
		req.Header.Set("X-Tenant-ID", owner)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		return ErrBusy
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrUnavailable
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return ErrUnavailable
	}
	if len(payload) > limit {
		return ErrTooLarge
	}
	if err := json.Unmarshal(payload, result); err != nil {
		return fmt.Errorf("%w: invalid service response", ErrFailed)
	}
	return nil
}

type ownerContextKey struct{}
