package agent

import (
	"context"
	"strings"
	"time"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/commands"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/constants"
	"github.com/xibodev/compa/pkg/logger"
)

// scheduledTurnKey marks the context of a turn a schedule started.
type scheduledTurnKey struct{}

// withScheduledTurn marks ctx as a scheduled (cron) turn's: its message is
// text stored in a job, replayed on every run, not someone's command.
func withScheduledTurn(ctx context.Context) context.Context {
	return context.WithValue(ctx, scheduledTurnKey{}, true)
}

func isScheduledTurn(ctx context.Context) bool {
	scheduled, _ := ctx.Value(scheduledTurnKey{}).(bool)
	return scheduled
}

// senderIsOwner reports whether msg came from the instance's owner (see
// inboundFromOwner).
func senderIsOwner(msg bus.InboundMessage) bool {
	inbound := msg.Context
	if strings.TrimSpace(inbound.Channel) == "" {
		inbound.Channel = msg.Channel
	}
	return inboundFromOwner(&inbound)
}

// inboundFromOwner reports whether a message came from the instance's owner:
// a sender its channel matched to an explicit allow_from entry, or anyone on
// the web UI or an internal channel such as the terminal.
func inboundFromOwner(inbound *bus.InboundContext) bool {
	if inbound == nil {
		return false
	}
	if inbound.SenderIsOwner {
		return true
	}
	channel := strings.TrimSpace(inbound.Channel)
	return channel == config.ChannelWeb || constants.IsInternalChannel(channel)
}

// commandsAllowed reports whether a slash command in msg may run. A
// scheduled turn never runs one -- a job whose message is "/reload" must not
// reload on every run -- and with commands.owner_only only the owner's run.
// Anyone else's command is plain text for the model.
func (al *AgentLoop) commandsAllowed(ctx context.Context, msg bus.InboundMessage) bool {
	if isScheduledTurn(ctx) {
		return false
	}
	cfg := al.GetConfig()
	if cfg == nil || !cfg.Commands.OwnerOnly {
		return true
	}
	return senderIsOwner(msg)
}

// logCommandRefused records, without the message, that a command was taken
// as text.
func logCommandRefused(ctx context.Context, msg bus.InboundMessage) {
	reason := "sender is not the owner (commands.owner_only)"
	if isScheduledTurn(ctx) {
		reason = "scheduled turn"
	}
	logger.InfoCF("agent", "Slash command treated as text", map[string]any{
		"channel": msg.Channel,
		"reason":  reason,
	})
}

// commandsDuringTurn are the commands that act while a turn of their session
// runs instead of joining it as steering: /btw is meant for that moment, and
// the others change what comes next.
var commandsDuringTurn = map[string]struct{}{
	"btw":    {},
	"clear":  {},
	"stop":   {},
	"switch": {},
	"use":    {},
}

// dispatchCommandDuringTurn runs msg's command when a turn of its session is
// active, reporting whether it took the message. Only commands the sender
// may run are taken; anything else joins the turn as steering. The command
// runs off the receive loop: /btw asks the model.
func (al *AgentLoop) dispatchCommandDuringTurn(ctx context.Context, msg bus.InboundMessage, sessionKey string) bool {
	name, ok := commands.CommandName(msg.Content)
	if !ok {
		return false
	}
	if _, during := commandsDuringTurn[name]; !during {
		return false
	}
	if !al.commandsAllowed(ctx, msg) {
		logCommandRefused(ctx, msg)
		return false
	}
	// "/use <skill> <message>" starts a turn of its own; while one runs, the
	// message joins it.
	if name == "use" && len(strings.Fields(msg.Content)) > 2 {
		return false
	}
	if name == "stop" {
		// In order with the session's next messages: a stop armed for a
		// turn that has not started yet must be seen by its worker.
		return al.tryHandleStopCommand(ctx, msg, sessionKey)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.RecoverPanicNoExit(r)
			}
		}()
		al.runCommandDuringTurn(ctx, msg)
	}()
	return true
}

// runCommandDuringTurn runs a command of a session whose turn is active and
// sends its reply to the chat.
func (al *AgentLoop) runCommandDuringTurn(ctx context.Context, msg bus.InboundMessage) {
	msg = bus.NormalizeInboundMessage(msg)
	agent, opts, err := al.messageTurnOptions(msg)
	if err != nil {
		logger.WarnCF("agent", "Command during an active turn could not be routed",
			map[string]any{"channel": msg.Channel, "error": err.Error()})
		return
	}
	reply, handled := al.handleCommand(ctx, msg, agent, &opts)
	if !handled {
		reply = "That command can't run while I'm answering; send it again when I'm done."
	}
	al.publishDirectReply(ctx, msg, opts.Dispatch.SessionKey, reply)
}

// publishDirectReply sends text to the chat msg came from, keeping its
// account, topic and the message it answers. It is not subject to the
// message tool's same-chat de-duplication: it answers msg itself.
func (al *AgentLoop) publishDirectReply(ctx context.Context, msg bus.InboundMessage, sessionKey, text string) {
	if strings.TrimSpace(text) == "" || al.bus == nil {
		return
	}
	pubCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out := bus.OutboundMessage{
		Context:    outboundContextFromInbound(&msg.Context, msg.Channel, msg.ChatID, msg.Context.MessageID),
		SessionKey: sessionKey,
		Content:    text,
	}
	if err := al.bus.PublishOutbound(pubCtx, out); err != nil {
		logger.WarnCF("agent", "Failed to send a reply", map[string]any{
			"channel": msg.Channel,
			"error":   err.Error(),
		})
	}
}

// steeringQueueFullReply tells a sender their message was dropped.
const steeringQueueFullReply = "I'm still working through your earlier messages, so this one wasn't queued. " +
	"Send it again once I reply."
