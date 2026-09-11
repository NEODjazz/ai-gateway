package provider

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

	"golang.org/x/net/websocket"
)

const MaxRealtimeEventBytes = 4 << 20
const MaxRealtimeSessionDuration = 30 * time.Minute

type RealtimeConnection interface {
	Send([]byte) error
	Receive() ([]byte, error)
	Close() error
}

type RealtimeClient interface {
	OpenRealtime(context.Context, string) (RealtimeConnection, error)
}

type RealtimeTokenReserver interface {
	ReserveRealtimeTokens(context.Context, int) error
}

type websocketRealtimeConnection struct {
	conn *websocket.Conn
}

func (p OpenAICompatible) OpenRealtime(ctx context.Context, model string) (RealtimeConnection, error) {
	model = strings.TrimSpace(model)
	if model == "" || len(model) > 256 {
		return nil, errors.New("realtime model is required and must not exceed 256 bytes")
	}
	endpoint, err := url.Parse(providerURL(p.baseURL, "realtime"))
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, errors.New("invalid realtime provider URL")
	}
	if endpoint.Scheme == "https" {
		endpoint.Scheme = "wss"
	} else {
		endpoint.Scheme = "ws"
	}
	query := endpoint.Query()
	query.Set("model", model)
	endpoint.RawQuery = query.Encode()
	origin := *endpoint
	if origin.Scheme == "wss" {
		origin.Scheme = "https"
	} else {
		origin.Scheme = "http"
	}
	origin.Path, origin.RawQuery, origin.Fragment = "", "", ""
	config, err := websocket.NewConfig(endpoint.String(), origin.String())
	if err != nil {
		return nil, errors.New("invalid realtime websocket configuration")
	}
	config.Header = make(http.Header)
	if p.apiKey != "" {
		config.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	config.Header.Set("OpenAI-Beta", "realtime=v1")
	conn, err := config.DialContext(ctx)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("open realtime websocket: %w", ctxErr)
		}
		return nil, fmt.Errorf("open realtime websocket: %w", err)
	}
	conn.MaxPayloadBytes = MaxRealtimeEventBytes
	if err := conn.SetDeadline(time.Now().Add(MaxRealtimeSessionDuration)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bound realtime websocket lifetime: %w", err)
	}
	return &websocketRealtimeConnection{conn: conn}, nil
}

func (c *websocketRealtimeConnection) Send(payload []byte) error {
	if err := validateRealtimeEvent(payload); err != nil {
		return err
	}
	return boundedRealtimeCodec.Send(c.conn, payload)
}

func (c *websocketRealtimeConnection) Receive() ([]byte, error) {
	var payload []byte
	if err := boundedRealtimeCodec.Receive(c.conn, &payload); err != nil {
		return nil, err
	}
	if err := validateRealtimeEvent(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (c *websocketRealtimeConnection) Close() error { return c.conn.Close() }

var boundedRealtimeCodec = websocket.Codec{
	Marshal: func(value any) ([]byte, byte, error) {
		payload, ok := value.([]byte)
		if !ok || len(payload) > MaxRealtimeEventBytes {
			return nil, websocket.TextFrame, errors.New("realtime event exceeds its size limit")
		}
		return payload, websocket.TextFrame, nil
	},
	Unmarshal: func(payload []byte, frameType byte, value any) error {
		if frameType != websocket.TextFrame {
			return errors.New("realtime events must use text frames")
		}
		if len(payload) > MaxRealtimeEventBytes {
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

func validateRealtimeEvent(payload []byte) error {
	if len(payload) == 0 || len(payload) > MaxRealtimeEventBytes {
		return errors.New("realtime event exceeds its size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(bytes.NewReader(payload), MaxRealtimeEventBytes+1))
	var envelope struct {
		Type string `json:"type"`
	}
	if err := decoder.Decode(&envelope); err != nil || strings.TrimSpace(envelope.Type) == "" || len(envelope.Type) > 256 {
		return errors.New("realtime event requires a bounded type")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("realtime event must contain one JSON object")
	}
	return nil
}
