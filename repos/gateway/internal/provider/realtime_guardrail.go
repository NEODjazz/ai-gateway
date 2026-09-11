package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ai-gateway-gateway/internal/modules"
	"ai-gateway-gateway/internal/openai"
)

const maxRealtimeGuardrailBufferBytes = 8 << 20

type guardedRealtimeConnection struct {
	ctx        context.Context
	connection RealtimeConnection
	pipeline   modules.Pipeline
	request    modules.RequestContext
	buffer     [][]byte
	bufferSize int
	ready      [][]byte
	active     int
}

type realtimeGuardrailEvent struct {
	Type     string          `json:"type"`
	Response json.RawMessage `json:"response"`
}

func newGuardedRealtimeConnection(ctx context.Context, connection RealtimeConnection, pipeline modules.Pipeline, request modules.RequestContext) RealtimeConnection {
	return &guardedRealtimeConnection{ctx: ctx, connection: connection, pipeline: pipeline, request: request}
}

func (c *guardedRealtimeConnection) Send(payload []byte) error {
	return c.connection.Send(payload)
}

func (c *guardedRealtimeConnection) Receive() ([]byte, error) {
	if len(c.ready) > 0 {
		return c.popReady(), nil
	}
	for {
		payload, err := c.connection.Receive()
		if err != nil {
			return nil, err
		}
		var event realtimeGuardrailEvent
		if err := json.Unmarshal(payload, &event); err != nil || event.Type == "" {
			return nil, errors.New("invalid realtime provider event")
		}
		if c.active == 0 && event.Type != "response.created" && event.Type != "response.done" {
			return payload, nil
		}
		if event.Type == "response.done" && c.active == 0 {
			if err := c.scanResponse(event.Response); err != nil {
				return nil, err
			}
			return payload, nil
		}
		if event.Type == "error" && c.active > 0 {
			c.buffer = nil
			c.bufferSize = 0
			c.active = 0
			return payload, nil
		}
		if err := c.appendBuffered(payload); err != nil {
			return nil, err
		}
		switch event.Type {
		case "response.created":
			c.active++
		case "response.done":
			if err := c.scanResponse(event.Response); err != nil {
				return nil, err
			}
			if c.active > 0 {
				c.active--
			}
			if c.active == 0 {
				c.ready, c.buffer = c.buffer, nil
				c.bufferSize = 0
				return c.popReady(), nil
			}
		}
	}
}

func (c *guardedRealtimeConnection) ReserveRealtimeTokens(ctx context.Context, tokens int) error {
	reserver, ok := c.connection.(RealtimeTokenReserver)
	if !ok {
		return nil
	}
	return reserver.ReserveRealtimeTokens(ctx, tokens)
}

func (c *guardedRealtimeConnection) Close() error {
	return c.connection.Close()
}

func (c *guardedRealtimeConnection) appendBuffered(payload []byte) error {
	if len(payload) > maxRealtimeGuardrailBufferBytes-c.bufferSize {
		return fmt.Errorf("realtime output exceeds guardrail buffer: %w", modules.ErrGuardrailUnavailable)
	}
	c.buffer = append(c.buffer, append([]byte(nil), payload...))
	c.bufferSize += len(payload)
	return nil
}

func (c *guardedRealtimeConnection) popReady() []byte {
	payload := c.ready[0]
	c.ready = c.ready[1:]
	return payload
}

func (c *guardedRealtimeConnection) scanResponse(payload json.RawMessage) error {
	var response openai.ResponseResponse
	if len(payload) == 0 || json.Unmarshal(payload, &response) != nil || response.ID == "" {
		return fmt.Errorf("invalid realtime response for output guardrail: %w", modules.ErrGuardrailUnavailable)
	}
	request := c.request
	request.ResponsesResponse = &response
	return c.pipeline.RunNamedPostResponse(c.ctx, &request, "dlp")
}
