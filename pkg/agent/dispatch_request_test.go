package agent

import (
	"testing"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/session"
)

func TestDispatchRequest_AddressingComesFromInboundContext(t *testing.T) {
	dispatch := DispatchRequest{
		SessionKey: "sk_v1_example",
		InboundContext: &bus.InboundContext{
			Channel:          "slack",
			ChatID:           "C123",
			ChatType:         "channel",
			SenderID:         "U123",
			MessageID:        "m-1",
			ReplyToMessageID: "parent-1",
			Raw:              map[string]string{bus.MetadataKeySelectedModule: " archive "},
		},
	}

	if dispatch.Channel() != "slack" || dispatch.ChatID() != "C123" {
		t.Fatalf("addressing = (%q,%q), want (slack,C123)", dispatch.Channel(), dispatch.ChatID())
	}
	if dispatch.SenderID() != "U123" || dispatch.MessageID() != "m-1" {
		t.Fatalf("sender/message = (%q,%q), want (U123,m-1)", dispatch.SenderID(), dispatch.MessageID())
	}
	if dispatch.ReplyToMessageID() != "parent-1" {
		t.Fatalf("ReplyToMessageID() = %q, want parent-1", dispatch.ReplyToMessageID())
	}
	if dispatch.SelectedModule() != "archive" {
		t.Fatalf("SelectedModule() = %q, want archive", dispatch.SelectedModule())
	}
}

func TestDispatchRequest_WithoutInboundContextHasNoAddressing(t *testing.T) {
	var dispatch DispatchRequest
	if dispatch.Channel() != "" || dispatch.ChatID() != "" || dispatch.SenderID() != "" ||
		dispatch.MessageID() != "" || dispatch.ReplyToMessageID() != "" || dispatch.SelectedModule() != "" {
		t.Fatalf("zero DispatchRequest reported addressing: %+v", dispatch)
	}
}

func TestInferChatTypeFromSessionScope(t *testing.T) {
	tests := []struct {
		name  string
		scope *session.SessionScope
		want  string
	}{
		{name: "nil scope", scope: nil, want: ""},
		{name: "no chat dimension", scope: &session.SessionScope{Values: map[string]string{"sender": "u1"}}, want: ""},
		{name: "chat without type", scope: &session.SessionScope{Values: map[string]string{"chat": "c1"}}, want: ""},
		{name: "group chat", scope: &session.SessionScope{Values: map[string]string{"chat": "Group:-100123"}}, want: "group"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inferChatTypeFromSessionScope(tt.scope); got != tt.want {
				t.Fatalf("inferChatTypeFromSessionScope() = %q, want %q", got, tt.want)
			}
		})
	}
}
