package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxRemoteResponseBytes = 8 << 20

func newRemoteHTTPClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Second}
}

func callRemote[Request any, Response any](ctx context.Context, client *http.Client, endpoint string, request Request) (Response, error) {
	var result Response
	body, err := json.Marshal(request)
	if err != nil {
		return result, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return result, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return result, ErrUnauthorized
	}
	if resp.StatusCode == http.StatusUnavailableForLegalReasons {
		return result, ErrContentRejected
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("remote endpoint returned %s", resp.Status)
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, maxRemoteResponseBytes)).Decode(&result); err != nil {
		return result, err
	}
	return result, nil
}
