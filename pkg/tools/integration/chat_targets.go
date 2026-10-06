package integrationtools

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/xibodev/compa/v2/pkg/config"
)

// chatTargets is the tools.message.targets policy of a tool that writes to
// chats. Until SetTargets is called, any chat may be reached, as before the
// setting existed.
type chatTargets struct {
	currentChatOnly atomic.Bool
}

// SetTargets sets which chats the tool may reach: config.MessageTargetsAny
// lets it reach any chat of any channel; config.MessageTargetsCurrentChat,
// or any other value, only the chat of the turn. Pass
// cfg.Tools.Message.EffectiveTargets().
func (c *chatTargets) SetTargets(targets string) {
	c.currentChatOnly.Store(strings.TrimSpace(targets) != config.MessageTargetsAny)
}

// resolve fills an omitted channel or chat ID with the turn's and, when only
// the current chat may be reached, refuses any other chat. The model writes
// the arguments, so they cannot widen what the owner allowed.
func (c *chatTargets) resolve(ctx context.Context, tool, channel, chatID string) (string, string, error) {
	turnChannel, turnChatID := ToolChannel(ctx), ToolChatID(ctx)
	channel, chatID = strings.TrimSpace(channel), strings.TrimSpace(chatID)
	if channel == "" || strings.EqualFold(channel, turnChannel) {
		channel = turnChannel
	}
	if chatID == "" {
		chatID = turnChatID
	}
	if !c.currentChatOnly.Load() {
		return channel, chatID, nil
	}
	if turnChannel == "" || turnChatID == "" {
		return "", "", fmt.Errorf(
			"%s can only reach the current chat (tools.message.targets is %q), and this turn has none",
			tool, config.MessageTargetsCurrentChat)
	}
	if channel != turnChannel || chatID != turnChatID {
		return "", "", fmt.Errorf(
			"%s can only reach the current chat (tools.message.targets is %q); "+
				"omit channel and chat_id to use it",
			tool, config.MessageTargetsCurrentChat)
	}
	return channel, chatID, nil
}
