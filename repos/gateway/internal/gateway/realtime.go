package gateway

import (
	"errors"
	"net/http"
	"strings"
	"sync"

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
	if !h.prepareAccessGroups(w, &identity) || !h.authorizeAccess(w, r.Context(), identity, model, 0) || !h.prepareModelFallbacks(w, r.Context(), &identity, model) {
		return
	}
	runtime, ok := h.provider.(provider.RealtimeProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "unsupported_operation", "realtime sessions are not supported")
		return
	}
	upstream, _, err := runtime.OpenRealtime(r.Context(), identity, model)
	if err != nil {
		writeProviderFailure(w, err)
		return
	}
	defer upstream.Close()

	websocket.Handler(func(client *websocket.Conn) {
		client.MaxPayloadBytes = provider.MaxRealtimeEventBytes
		proxyRealtime(client, upstream)
	}).ServeHTTP(w, r)
}

func realtimeModelRequest(model string) (request openai.ChatCompletionRequest) {
	request.Model = model
	return request
}

func proxyRealtime(client *websocket.Conn, upstream provider.RealtimeConnection) {
	errors := make(chan error, 2)
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
				err = websocket.Message.Send(client, string(payload))
			}
			if err != nil {
				errors <- err
				return
			}
		}
	}()
	go func() {
		for {
			var payload []byte
			err := realtimeClientCodec.Receive(client, &payload)
			if err == nil {
				err = upstream.Send(payload)
			}
			if err != nil {
				errors <- err
				return
			}
		}
	}()
	<-errors
	closeBoth()
	<-errors
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
