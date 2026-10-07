// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/constants"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/tools"
	"github.com/xibodev/compa/v3/pkg/utils"
)

// maybePublishErrorTo tells target's chat that its message failed, unless
// the turn was canceled, and reports whether it did. The details go to the
// log; see processingErrorReply for what the chat is told.
func (al *AgentLoop) maybePublishErrorTo(ctx context.Context, target *continuationTarget, err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	if target == nil {
		target = &continuationTarget{}
	}
	logger.ErrorCF("agent", "Failed to process message", map[string]any{
		"channel":     target.Channel,
		"chat_id":     target.ChatID,
		"session_key": target.SessionKey,
		"error":       err.Error(),
	})
	al.publishTargetResponse(ctx, target, processingErrorReply(target.Channel, err))
	return true
}

// processingErrorReply is what a chat is told when its message fails. The
// web UI and the terminal, which the owner uses on this machine, get the
// details. Other chats, which other people may read, get a short message
// without endpoints or paths: the details are in the log.
func processingErrorReply(channel string, err error) string {
	channel = strings.TrimSpace(channel)
	if channel == "" || channel == config.ChannelWeb || constants.IsInternalChannel(channel) {
		return formatProcessingError(err)
	}
	if friendly := providerFailureMessage(err); friendly != "" {
		return "Sorry, I couldn't answer: " + friendly
	}
	var noModel *noModelError
	if errors.As(err, &noModel) {
		return "Sorry, I couldn't answer: no model is set up for me yet."
	}
	return remoteProcessingErrorReply
}

// remoteProcessingErrorReply answers a failed message in a chat other people
// may read.
const remoteProcessingErrorReply = "Sorry, something went wrong while answering. The details are in Compa's log."

// PublishResponseIfNeeded sends response to the chat, unless the session's
// agent already wrote to it with its message tool: the cron tool's reply.
func (al *AgentLoop) PublishResponseIfNeeded(ctx context.Context, channel, chatID, sessionKey, response string) {
	al.publishTargetResponse(ctx, &continuationTarget{SessionKey: sessionKey, Channel: channel, ChatID: chatID}, response)
}

// publishTargetResponse sends a turn's final reply to target's chat, keeping
// the account, topic and the message it answers from target's context. It
// sends nothing when the agent's message tool already wrote to that chat in
// this round.
func (al *AgentLoop) publishTargetResponse(ctx context.Context, target *continuationTarget, response string) {
	if response == "" || target == nil {
		return
	}
	channel, chatID, sessionKey := target.Channel, target.ChatID, target.SessionKey

	var agent *AgentInstance
	if target.AgentID != "" {
		if registry := al.GetRegistry(); registry != nil {
			agent, _ = registry.GetAgent(target.AgentID)
		}
	}
	if agent == nil && sessionKey != "" {
		agent = al.agentForSession(sessionKey)
	}
	if agent == nil {
		agent = al.GetRegistry().GetDefaultAgent()
	}

	// The message tool of the agent that ran the turn knows what it sent.
	alreadySentToSameChat := false
	if agent != nil {
		if tool, ok := agent.Tools.Get("message"); ok {
			if mt, ok := tool.(*tools.MessageTool); ok {
				alreadySentToSameChat = mt.HasSentTo(sessionKey, channel, chatID)
			}
		}
	}

	if alreadySentToSameChat {
		if cm := al.currentChannelManager(); cm != nil && channel != "" && chatID != "" {
			dismissCtx, dismissCancel := context.WithTimeout(ctx, 5*time.Second)
			cm.DismissToolFeedback(
				dismissCtx,
				channel,
				chatID,
				nil,
			)
			dismissCancel()
		}
		logger.DebugCF(
			"agent",
			"Skipped outbound (message tool already sent to same chat)",
			map[string]any{"channel": channel, "chat_id": chatID},
		)
		return
	}

	replyTo := ""
	if target.Inbound != nil {
		replyTo = target.Inbound.ReplyToMessageID
	}
	msg := bus.OutboundMessage{
		Context:    outboundContextFromInbound(target.Inbound, channel, chatID, replyTo),
		SessionKey: sessionKey,
		Content:    response,
	}
	if agent != nil && agent.Sessions != nil && sessionKey != "" {
		msg.AgentID = agent.ID
		history := agent.Sessions.GetHistory(sessionKey)
		for index := len(history) - 1; index >= 0; index-- {
			assistant := history[index]
			if assistant.Role != "assistant" || assistant.Content != response {
				continue
			}
			if msg.Context.Raw == nil {
				msg.Context.Raw = make(map[string]string, 4)
			}
			if assistant.ModelName != "" {
				msg.Context.Raw["model_name"] = assistant.ModelName
			}
			for key, value := range compactSelectionMetadata(
				assistant.RequestedSelection,
				assistant.ServedTarget,
				assistant.ServedIdentity,
			) {
				msg.Context.Raw[key] = value
			}
			break
		}
		msg.ContextUsage = computeContextUsage(agent, sessionKey)
	}
	markFinalOutbound(&msg)
	pubCtx, pubCancel := context.WithTimeout(ctx, outboundPublishTimeout)
	defer pubCancel()
	if err := al.bus.PublishOutbound(pubCtx, msg); err != nil {
		logger.WarnCF("agent", "Failed to publish outbound response",
			map[string]any{"channel": channel, "chat_id": chatID, "error": err.Error()})
		return
	}
	logger.InfoCF("agent", "Published outbound response",
		map[string]any{
			"channel":     channel,
			"chat_id":     chatID,
			"content_len": len(response),
		})
}

// outboundPublishTimeout bounds how long a final reply waits for room on the
// outbound bus.
const outboundPublishTimeout = 30 * time.Second

func (al *AgentLoop) targetReasoningChannelID(channelName string) (chatID string) {
	if al.currentChannelManager() == nil {
		return ""
	}
	if ch, ok := al.currentChannelManager().GetChannel(channelName); ok {
		return ch.ReasoningChannelID()
	}
	return ""
}

func (al *AgentLoop) publishWebReasoning(
	ctx context.Context,
	reasoningContent, chatID, sessionKey, modelName string,
) {
	if reasoningContent == "" || chatID == "" {
		return
	}

	if ctx.Err() != nil {
		return
	}

	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	raw := map[string]string{metadataKeyMessageKind: messageKindThought}
	if trimmedModelName := strings.TrimSpace(modelName); trimmedModelName != "" {
		raw["model_name"] = trimmedModelName
	}

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Context: bus.InboundContext{
			Channel: "web",
			ChatID:  chatID,
			Raw:     raw,
		},
		SessionKey: sessionKey,
		Content:    reasoningContent,
	}); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			logger.DebugCF("agent", "Web chat reasoning publish skipped (timeout/cancel)", map[string]any{
				"channel": "web",
				"error":   err.Error(),
			})
		} else {
			logger.WarnCF("agent", "Failed to publish web reasoning (best-effort)", map[string]any{
				"channel": "web",
				"error":   err.Error(),
			})
		}
	}
}

func (al *AgentLoop) publishWebToolCallInterim(
	ctx context.Context,
	ts *turnState,
	modelName string,
	reasoningContent string,
	content string,
	toolCalls []providers.ToolCall,
) {
	if ts == nil || ts.chatID == "" || al == nil || al.bus == nil {
		return
	}

	if strings.TrimSpace(reasoningContent) != "" {
		pubCtx, pubCancel := context.WithTimeout(ctx, 3*time.Second)
		err := al.bus.PublishOutbound(
			pubCtx,
			outboundMessageForTurnWithOptions(
				ts,
				reasoningContent,
				outboundTurnMessageOptions{
					kind:      messageKindThought,
					modelName: modelName,
				},
			),
		)
		pubCancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, bus.ErrBusClosed) {
			logger.WarnCF("agent", "Failed to publish web reasoning", map[string]any{
				"channel": ts.channel,
				"chat_id": ts.chatID,
				"error":   err.Error(),
			})
		}
	}

	if !ts.opts.AllowInterimWebPublish {
		return
	}

	visibleToolCalls := utils.BuildVisibleToolCalls(
		toolCalls,
		al.GetConfig().Agents.Defaults.GetToolFeedbackMaxArgsLength(),
	)
	duplicateToolCallContent := len(visibleToolCalls) > 0 &&
		utils.ToolCallExplanationDuplicatesContent(content, toolCalls)

	if strings.TrimSpace(content) != "" && !duplicateToolCallContent {
		pubCtx, pubCancel := context.WithTimeout(ctx, 3*time.Second)
		err := al.bus.PublishOutbound(
			pubCtx,
			outboundMessageForTurnWithOptions(ts, content, outboundTurnMessageOptions{
				modelName: modelName,
			}),
		)
		pubCancel()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, context.Canceled) &&
			!errors.Is(err, bus.ErrBusClosed) {
			logger.WarnCF("agent", "Failed to publish web interim assistant content", map[string]any{
				"channel": ts.channel,
				"chat_id": ts.chatID,
				"error":   err.Error(),
			})
		}
	}

	if len(visibleToolCalls) == 0 {
		return
	}

	rawToolCalls, err := json.Marshal(visibleToolCalls)
	if err != nil {
		logger.WarnCF("agent", "Failed to serialize web tool calls", map[string]any{
			"channel": ts.channel,
			"chat_id": ts.chatID,
			"error":   err.Error(),
		})
		return
	}

	msg := outboundMessageForTurnWithOptions(ts, "", outboundTurnMessageOptions{
		kind:      messageKindToolCalls,
		modelName: modelName,
		raw: map[string]string{
			metadataKeyToolCalls: string(rawToolCalls),
		},
	})

	pubCtx, pubCancel := context.WithTimeout(ctx, 3*time.Second)
	err = al.bus.PublishOutbound(pubCtx, msg)
	pubCancel()
	if err != nil && !errors.Is(err, context.DeadlineExceeded) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, bus.ErrBusClosed) {
		logger.WarnCF("agent", "Failed to publish web tool calls", map[string]any{
			"channel": ts.channel,
			"chat_id": ts.chatID,
			"error":   err.Error(),
		})
	}
}

func (al *AgentLoop) handleReasoning(
	ctx context.Context,
	reasoningContent, channelName, channelID string,
) {
	if reasoningContent == "" || channelName == "" || channelID == "" {
		return
	}

	// Check context cancellation before attempting to publish,
	// since PublishOutbound's select may race between send and ctx.Done().
	if ctx.Err() != nil {
		return
	}

	// Use a short timeout so the goroutine does not block indefinitely when
	// the outbound bus is full.  Reasoning output is best-effort; dropping it
	// is acceptable to avoid goroutine accumulation.
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()

	if err := al.bus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Context: bus.NewOutboundContext(channelName, channelID, ""),
		Content: reasoningContent,
	}); err != nil {
		// Treat context.DeadlineExceeded / context.Canceled as expected
		// (bus full under load, or parent canceled).  Check the error
		// itself rather than ctx.Err(), because pubCtx may time out
		// (5 s) while the parent ctx is still active.
		// Also treat ErrBusClosed as expected — it occurs during normal
		// shutdown when the bus is closed before all goroutines finish.
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			errors.Is(err, bus.ErrBusClosed) {
			logger.DebugCF("agent", "Reasoning publish skipped (timeout/cancel)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		} else {
			logger.WarnCF("agent", "Failed to publish reasoning (best-effort)", map[string]any{
				"channel": channelName,
				"error":   err.Error(),
			})
		}
	}
}
