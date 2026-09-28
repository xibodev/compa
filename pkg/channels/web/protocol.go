package web

import (
	"strings"
	"time"
)

// Protocol message types.
const (
	// TypeMessageSend is sent from client to server.
	TypeMessageSend = "message.send"
	TypeMediaSend   = "media.send"
	TypePing        = "ping"

	// TypeMessageCreate is sent from server to client.
	TypeMessageCreate = "message.create"
	TypeMessageUpdate = "message.update"
	TypeMessageDelete = "message.delete"
	TypeMediaCreate   = "media.create"
	TypeTypingStart   = "typing.start"
	TypeTypingStop    = "typing.stop"
	TypeError         = "error"
	TypePong          = "pong"

	PayloadKeyContent        = "content"
	PayloadKeyKind           = "kind"
	PayloadKeyPlaceholder    = "placeholder"
	PayloadKeyToolCalls      = "tool_calls"
	PayloadKeyModelName      = "model_name"
	PayloadKeySelection      = "selection"
	PayloadKeyServedTarget   = "served_target"
	PayloadKeyServedIdentity = "served_identity"
	PayloadKeyUsage          = "usage"

	MessageKindThought   = "thought"
	MessageKindToolCalls = "tool_calls"
)

// WebMessage is the wire format for all web chat protocol messages.
type WebMessage struct {
	Type      string         `json:"type"`
	ID        string         `json:"id,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	Timestamp int64          `json:"timestamp,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// newMessage creates a WebMessage with the given type and payload.
func newMessage(msgType string, payload map[string]any) WebMessage {
	return WebMessage{
		Type:      msgType,
		Timestamp: time.Now().UnixMilli(),
		Payload:   payload,
	}
}

func isThoughtPayload(payload map[string]any) bool {
	kind, _ := payload[PayloadKeyKind].(string)
	return strings.EqualFold(strings.TrimSpace(kind), MessageKindThought)
}

func newErrorWithPayload(code, message string, extra map[string]any) WebMessage {
	payload := map[string]any{
		"code":    code,
		"message": message,
	}
	for key, value := range extra {
		payload[key] = value
	}
	return newMessage(TypeError, payload)
}

// newError creates an error WebMessage.
func newError(code, message string) WebMessage {
	return newErrorWithPayload(code, message, nil)
}
