// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/logger"
)

// processMessageSync runs msg and sends its reply or error to its chat; the
// caller provides the worker slot.
func (al *AgentLoop) processMessageSync(ctx context.Context, msg bus.InboundMessage) {
	if cm := al.currentChannelManager(); cm != nil {
		defer cm.InvokeTypingStop(msg.Channel, msg.ChatID)
	}

	response, err := al.processMessage(ctx, msg)
	target := al.continuationTargetFor(msg, msg.SessionKey, "")
	if err != nil {
		if !al.maybePublishErrorTo(ctx, target, err) {
			return
		}
		response = ""
	}
	al.publishTargetResponse(ctx, target, response)
}

func (al *AgentLoop) runTurnWithSteering(ctx context.Context, initialMsg bus.InboundMessage) {
	// Build continuation target
	target, targetErr := al.buildContinuationTarget(initialMsg)

	// Process the initial message. A channel that observes turns hears when
	// the turn starts, and that it ended once its reply is out.
	notices := al.newTurnNotices(initialMsg.Channel, initialMsg.ChatID, &initialMsg.Context)
	response, err := al.processMessage(withTurnNotices(ctx, notices), initialMsg)
	if err != nil {
		errTarget := target
		if errTarget == nil {
			errTarget = al.continuationTargetFor(initialMsg, initialMsg.SessionKey, "")
		}
		if !al.maybePublishErrorTo(ctx, errTarget, err) {
			notices.end(ctx)
			return // context canceled
		}
		response = ""
	}

	if targetErr != nil {
		logger.WarnCF("agent", "Failed to build steering continuation target",
			map[string]any{
				"channel": initialMsg.Channel,
				"error":   targetErr.Error(),
			})
		notices.end(ctx)
		return
	}

	// The turn's answer goes out before the replies to steering that came
	// after it, as an answer that steering overtakes mid-turn does
	// (keepAnswerBeforeSteering): the user gets every answer, in order.
	if response != "" {
		al.publishTargetResponse(ctx, target, response)
	}
	notices.end(ctx)
	if err := al.drainQueuedSteeringContinuations(ctx, target); err != nil {
		logger.WarnCF("agent", "Failed to continue queued steering",
			map[string]any{
				"channel": target.Channel,
				"chat_id": target.ChatID,
				"error":   err.Error(),
			})
	}
}

// drainQueuedSteeringContinuations continues target's session while steering
// waits for it, delivering each continuation's reply to target.
func (al *AgentLoop) drainQueuedSteeringContinuations(
	ctx context.Context,
	target *continuationTarget,
) error {
	if target == nil {
		return nil
	}

	for al.pendingSteeringCountForScope(target.SessionKey) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}

		logger.InfoCF("agent", "Continuing queued steering after turn end",
			map[string]any{
				"channel":     target.Channel,
				"chat_id":     target.ChatID,
				"session_key": target.SessionKey,
				"queue_depth": al.pendingSteeringCountForScope(target.SessionKey),
			})

		notices := al.newTurnNotices(target.Channel, target.ChatID, target.Inbound)
		continued, err := al.continueTarget(withTurnNotices(ctx, notices), target)
		if err != nil {
			notices.end(ctx)
			return err
		}
		if continued == "" {
			notices.end(ctx)
			break
		}
		al.publishTargetResponse(ctx, target, continued)
		notices.end(ctx)
	}

	return nil
}

func (al *AgentLoop) resolveSteeringTarget(msg bus.InboundMessage) (string, string, bool) {
	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil || agent == nil {
		return "", "", false
	}
	allocation := al.allocateRouteSession(route, msg)

	return resolveScopeKey(allocation.SessionKey, msg.SessionKey), agent.ID, true
}

// continuationTargetFor addresses the replies of sessionKey's turns to the
// chat msg came from, keeping its context.
func (al *AgentLoop) continuationTargetFor(msg bus.InboundMessage, sessionKey, agentID string) *continuationTarget {
	return &continuationTarget{
		SessionKey: sessionKey,
		Channel:    msg.Channel,
		ChatID:     msg.ChatID,
		AgentID:    agentID,
		Inbound:    cloneInboundContext(&msg.Context),
	}
}
