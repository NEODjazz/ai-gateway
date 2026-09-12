package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
	"ai-gateway-gateway/internal/provider"
	"golang.org/x/net/websocket"
)

func (h Handler) Realtime(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Query()) != 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is the only supported query parameter")
		return
	}
	model := strings.TrimSpace(r.URL.Query().Get("model"))
	if model == "" || len(model) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_request", "model is required and must not exceed 256 bytes")
		return
	}
	identity := modules.RequestContext{
		APIKey:    bearerToken(r.Header.Get("Authorization")),
		RequestID: executionID(w),
		SessionID: sessionID(r),
		Request:   realtimeModelRequest(model),
		Metadata:  map[string]string{"gateway.api_type": "realtime"},
	}
	if err := h.pipeline.RunAuthentication(r.Context(), &identity); err != nil {
		if errors.Is(err, modules.ErrUnauthorized) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		writeError(w, http.StatusBadGateway, "module_failed", "authentication failed")
		return
	}
	identity.APIKey = ""
	if !h.prepareAccessGroups(w, &identity) || !h.authorizeModel(w, identity, model) || !h.prepareModelFallbacks(w, r.Context(), &identity, model) {
		return
	}
	runtime, ok := h.provider.(provider.RealtimeProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "realtime sessions are not supported")
		return
	}
	upstream, selected, err := runtime.OpenRealtime(r.Context(), identity, model)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	defer upstream.Close()
	tracker := newRealtimeBillingTracker(h.pipeline, selected, model, func(ctx context.Context, tokens int) error {
		allowed, retryAfter, err := h.checkRateLimit(ctx, identity, tokens)
		if err != nil {
			return fmt.Errorf("realtime rate limit unavailable: %w", err)
		}
		if !allowed {
			return &realtimeRateLimitError{retryAfter: retryAfter}
		}
		if reserver, ok := upstream.(provider.RealtimeTokenReserver); ok {
			return reserver.ReserveRealtimeTokens(ctx, tokens)
		}
		return nil
	})
	defer tracker.Close(r.Context(), errors.New("realtime session closed"))

	websocket.Handler(func(client *websocket.Conn) {
		client.MaxPayloadBytes = provider.MaxRealtimeEventBytes
		proxyRealtime(r.Context(), client, upstream, tracker)
	}).ServeHTTP(w, r)
}

func realtimeModelRequest(model string) (request openai.ChatCompletionRequest) {
	request.Model = model
	return request
}

func proxyRealtime(ctx context.Context, client *websocket.Conn, upstream provider.RealtimeConnection, tracker *realtimeBillingTracker) {
	terminal := make(chan error, 2)
	var closeOnce sync.Once
	closeBoth := func() {
		closeOnce.Do(func() {
			_ = client.Close()
			_ = upstream.Close()
		})
	}
	go func() {
		for {
			payload, err := upstream.Receive()
			if err == nil {
				err = tracker.ProviderEvent(ctx, payload)
			}
			if err == nil {
				err = websocket.Message.Send(client, string(payload))
			}
			if err != nil {
				if !isRealtimeDisconnect(err) {
					_ = sendRealtimeGatewayError(client, err)
				}
				terminal <- err
				return
			}
		}
	}()
	go func() {
		for {
			var payload []byte
			err := realtimeClientCodec.Receive(client, &payload)
			if err == nil {
				err = tracker.ClientEvent(ctx, payload)
			}
			if err == nil {
				err = upstream.Send(payload)
			}
			if err != nil {
				if !isRealtimeDisconnect(err) {
					_ = sendRealtimeGatewayError(client, err)
				}
				terminal <- err
				return
			}
		}
	}()
	cause := <-terminal
	closeBoth()
	<-terminal
	tracker.Close(ctx, cause)
}

func sendRealtimeGatewayError(client *websocket.Conn, cause error) error {
	code := "gateway_error"
	message := "realtime session failed"
	if errors.Is(cause, modules.ErrBudgetExceeded) {
		code, message = "budget_exceeded", "budget exceeded"
	} else if errors.Is(cause, modules.ErrBillingConflict) {
		code, message = "billing_conflict", "billing lifecycle conflict"
	} else if errors.Is(cause, errRealtimeInputTranscriptionUnsupported) {
		code, message = "unsupported_feature", errRealtimeInputTranscriptionUnsupported.Error()
	} else {
		var credentialLimit *realtimeRateLimitError
		var providerLimit *provider.ProviderQuotaError
		var deploymentLimit *provider.DeploymentQuotaError
		if errors.As(cause, &credentialLimit) || errors.As(cause, &providerLimit) || errors.As(cause, &deploymentLimit) {
			code, message = "rate_limit_exceeded", "rate limit exceeded"
		}
	}
	payload, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": "gateway_error", "code": code, "message": message}})
	return websocket.Message.Send(client, string(payload))
}

type realtimeRateLimitError struct{ retryAfter time.Duration }

func (e *realtimeRateLimitError) Error() string {
	return fmt.Sprintf("realtime rate limit exceeded; retry after %s", e.retryAfter)
}

func isRealtimeDisconnect(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled)
}

var realtimeClientCodec = websocket.Codec{
	Unmarshal: func(payload []byte, frameType byte, value any) error {
		if frameType != websocket.TextFrame {
			return errors.New("realtime events must use text frames")
		}
		if len(payload) == 0 || len(payload) > provider.MaxRealtimeEventBytes {
			return errors.New("realtime event exceeds its size limit")
		}
		target, ok := value.(*[]byte)
		if !ok {
			return errors.New("invalid realtime event target")
		}
		*target = append((*target)[:0], payload...)
		return nil
	},
}
