package integrationtools

import (
	"context"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
)

func newRecordingMessageTool(sent *[]string) *MessageTool {
	tool := NewMessageTool()
	tool.SetSendCallback(func(
		_ context.Context,
		channel, chatID, _, _ string,
		_ []bus.MediaPart,
	) error {
		*sent = append(*sent, channel+":"+chatID)
		return nil
	})
	return tool
}

func TestMessageTool_CurrentChatTargetsRefuseOtherChats(t *testing.T) {
	var sent []string
	tool := newRecordingMessageTool(&sent)
	tool.SetTargets(config.MessageTargetsCurrentChat)
	ctx := WithToolContext(context.Background(), "telegram", "100")

	for _, args := range []map[string]any{
		{"content": "hi", "channel": "telegram", "chat_id": "200"},
		{"content": "hi", "chat_id": "200"},
		{"content": "hi", "channel": "whatsapp"},
		{"content": "hi", "channel": "whatsapp", "chat_id": "100"},
	} {
		result := tool.Execute(ctx, args)
		if !result.IsError || !strings.Contains(result.ForLLM, "can only reach the current chat") {
			t.Fatalf("args %v: expected a refusal, got %+v", args, result)
		}
	}
	if len(sent) != 0 {
		t.Fatalf("messages were sent to other chats: %v", sent)
	}

	for _, args := range []map[string]any{
		{"content": "hi"},
		{"content": "hi", "channel": "telegram", "chat_id": "100"},
		{"content": "hi", "channel": "Telegram"},
	} {
		if result := tool.Execute(ctx, args); result.IsError {
			t.Fatalf("args %v: the current chat was refused: %s", args, result.ForLLM)
		}
	}
	if want := []string{"telegram:100", "telegram:100", "telegram:100"}; strings.Join(sent, ",") != strings.Join(want, ",") {
		t.Fatalf("sent = %v, want %v", sent, want)
	}

	// A turn without a chat has no current chat to reach.
	result := tool.Execute(context.Background(), map[string]any{
		"content": "hi", "channel": "telegram", "chat_id": "100",
	})
	if !result.IsError {
		t.Fatal("a turn without a chat sent a message to a chat the model named")
	}
}

func TestMessageTool_AnyTargetsAndUnsetKeepModelTargets(t *testing.T) {
	for _, configure := range []func(*MessageTool){
		func(tool *MessageTool) { tool.SetTargets(config.MessageTargetsAny) },
		func(*MessageTool) {}, // not configured: as before the setting
	} {
		var sent []string
		tool := newRecordingMessageTool(&sent)
		configure(tool)
		ctx := WithToolContext(context.Background(), "telegram", "100")
		result := tool.Execute(ctx, map[string]any{"content": "hi", "channel": "whatsapp", "chat_id": "200"})
		if result.IsError {
			t.Fatalf("send to another chat failed: %s", result.ForLLM)
		}
		if len(sent) != 1 || sent[0] != "whatsapp:200" {
			t.Fatalf("sent = %v", sent)
		}
	}
}

func TestReactionTool_FollowsMessageTargets(t *testing.T) {
	var reacted []string
	tool := NewReactionTool()
	tool.SetReactionCallback(func(_ context.Context, channel, chatID, messageID string) error {
		reacted = append(reacted, channel+":"+chatID+":"+messageID)
		return nil
	})
	tool.SetTargets(config.MessageTargetsCurrentChat)
	ctx := WithToolContext(context.Background(), "telegram", "100")

	result := tool.Execute(ctx, map[string]any{"channel": "telegram", "chat_id": "200", "message_id": "7"})
	if !result.IsError || !strings.Contains(result.ForLLM, "can only reach the current chat") {
		t.Fatalf("expected a refusal, got %+v", result)
	}
	if result := tool.Execute(ctx, map[string]any{"message_id": "7"}); result.IsError {
		t.Fatalf("reaction in the current chat failed: %s", result.ForLLM)
	}
	if len(reacted) != 1 || reacted[0] != "telegram:100:7" {
		t.Fatalf("reacted = %v", reacted)
	}

	tool.SetTargets(config.MessageTargetsAny)
	if result := tool.Execute(ctx, map[string]any{"channel": "telegram", "chat_id": "200", "message_id": "8"}); result.IsError {
		t.Fatalf("reaction elsewhere with targets any failed: %s", result.ForLLM)
	}
}
