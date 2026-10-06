// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/logger"
	"github.com/xibodev/compa/v2/pkg/routing"
	"github.com/xibodev/compa/v2/pkg/session"
	"github.com/xibodev/compa/v2/pkg/utils"
)

func (al *AgentLoop) buildContinuationTarget(msg bus.InboundMessage) (*continuationTarget, error) {
	route, agent, err := al.resolveMessageRoute(msg)
	if err != nil {
		return nil, err
	}
	allocation := al.allocateRouteSession(route, msg)

	return al.continuationTargetFor(msg, resolveScopeKey(allocation.SessionKey, msg.SessionKey), agent.ID), nil
}

// terminalChannel is the channel of the terminal's turns.
const terminalChannel = "cli"

// ProcessDirect runs content as a turn of the terminal's user, the owner:
// the CLI's direct and interactive modes.
func (al *AgentLoop) ProcessDirect(
	ctx context.Context,
	content, sessionKey string,
) (string, error) {
	return al.processDirect(ctx, content, sessionKey, terminalChannel, "direct")
}

// ProcessDirectWithChannel runs a scheduled job's message as a turn for the
// chat it names (cron's JobExecutor). The message is stored text replayed on
// every run, so slash commands in it never run.
func (al *AgentLoop) ProcessDirectWithChannel(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	return al.processDirect(withScheduledTurn(ctx), content, sessionKey, channel, chatID)
}

func (al *AgentLoop) processDirect(
	ctx context.Context,
	content, sessionKey, channel, chatID string,
) (string, error) {
	al.prepareExtensions(ctx)

	msg := bus.InboundMessage{
		Context: bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "cron",
		},
		Content:    content,
		SessionKey: sessionKey,
	}

	return al.processMessage(ctx, msg)
}

func (al *AgentLoop) ProcessHeartbeat(
	ctx context.Context,
	content, channel, chatID string,
) (string, error) {
	al.prepareExtensions(ctx)

	agent := al.GetRegistry().GetDefaultAgent()
	if agent == nil {
		return "", fmt.Errorf("no default agent for heartbeat")
	}
	if _, _, err := agent.primaryModel(); err != nil {
		// A heartbeat has no one to tell; it waits for a model rather than
		// failing every interval.
		logger.InfoCF("agent", "Heartbeat skipped: the default agent has no model",
			map[string]any{"agent_id": agent.ID, "reason": err.Error()})
		return "", nil
	}
	dispatch := DispatchRequest{
		SessionKey:  "heartbeat",
		UserMessage: content,
	}
	if channel != "" || chatID != "" {
		dispatch.InboundContext = &bus.InboundContext{
			Channel:  channel,
			ChatID:   chatID,
			ChatType: "direct",
			SenderID: "heartbeat",
		}
	}
	// A heartbeat, like a scheduled job, runs with nobody's message: the
	// owner's approvals go to the owner's chat (see turnSenderIsOwner).
	return al.runAgentLoop(withScheduledTurn(ctx), agent, processOptions{
		Dispatch:             dispatch,
		DefaultResponse:      defaultResponse,
		EnableSummary:        false,
		SendResponse:         false,
		SuppressToolFeedback: true,
		NoHistory:            true, // Don't load session history for heartbeat
	})
}

func (al *AgentLoop) prepareInboundMessageForAgent(
	ctx context.Context,
	msg bus.InboundMessage,
) bus.InboundMessage {
	msg = bus.NormalizeInboundMessage(msg)

	var hadAudio bool
	msg, hadAudio = al.transcribeAudioInMessage(ctx, msg)

	// For audio messages the placeholder was deferred by the channel.
	// Now that transcription (and optional feedback) is done, send it.
	if hadAudio && al.currentChannelManager() != nil {
		al.currentChannelManager().SendPlaceholder(ctx, msg.Channel, msg.ChatID)
	}

	return msg
}

func (al *AgentLoop) processMessage(ctx context.Context, msg bus.InboundMessage) (string, error) {
	msg = al.prepareInboundMessageForAgent(ctx, msg)

	// Messages hold what people write: INFO records only their size, and a
	// short preview goes to DEBUG.
	logger.InfoCF(
		"agent",
		fmt.Sprintf("Processing message from %s:%s", msg.Channel, msg.SenderID),
		map[string]any{
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"sender_id":   msg.SenderID,
			"session_key": msg.SessionKey,
			"content_len": len(msg.Content),
		},
	)
	logger.DebugCF("agent", "Message preview",
		map[string]any{"preview": utils.Truncate(msg.Content, 80)})

	agent, opts, err := al.messageTurnOptions(msg)
	if err != nil {
		return "", err
	}

	// Reset message-tool state for this round so we don't skip publishing due to a previous round.
	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(interface{ ResetSentInRound(sessionKey string) }); ok {
			resetter.ResetSentInRound(opts.Dispatch.SessionKey)
		}
	}

	// context-dependent commands check their own Runtime fields and report
	// "unavailable" when the required capability is nil.
	if response, handled := al.handleCommand(ctx, msg, agent, &opts); handled {
		return response, nil
	}

	if pending := al.takePendingSkills(opts.Dispatch.SessionKey); len(pending) > 0 {
		opts.ForcedSkills = append(opts.ForcedSkills, pending...)
		logger.InfoCF("agent", "Applying pending skill override",
			map[string]any{
				"session_key": opts.Dispatch.SessionKey,
				"skills":      strings.Join(pending, ","),
			})
	}
	if selection := opts.Dispatch.ModelSelection(); selection != "" {
		resolved, err := al.resolveTurnSelection(selection)
		if err != nil {
			return "", fmt.Errorf("model selection %q is not available: %w", selection, err)
		}
		opts.RequestedSelection = selection
		opts.InstanceResolution = resolved
	}

	return al.runAgentLoop(ctx, agent, opts)
}

// messageTurnOptions routes msg to its agent and session and builds the
// options of its turn.
func (al *AgentLoop) messageTurnOptions(msg bus.InboundMessage) (*AgentInstance, processOptions, error) {
	route, agent, routeErr := al.resolveMessageRoute(msg)
	if routeErr != nil {
		return nil, processOptions{}, routeErr
	}

	allocation := al.allocateRouteSession(route, msg)

	// Resolve session key from the route allocation, while honouring an opaque
	// session key supplied by the caller.
	sessionKey := resolveScopeKey(allocation.SessionKey, msg.SessionKey)

	logger.InfoCF("agent", "Routed message",
		map[string]any{
			"agent_id":      agent.ID,
			"session_key":   sessionKey,
			"matched_by":    route.MatchedBy,
			"route_agent":   route.AgentID,
			"route_channel": route.Channel,
		})

	opts := processOptions{
		Dispatch: DispatchRequest{
			SessionKey:     sessionKey,
			InboundContext: cloneInboundContext(&msg.Context),
			RouteResult:    cloneResolvedRoute(&route),
			SessionScope:   session.CloneScope(&allocation.Scope),
			UserMessage:    msg.Content,
			Media:          append([]string(nil), msg.Media...),
		},
		SenderDisplayName:      sanitizeSenderDisplayName(msg.Sender.DisplayName),
		DefaultResponse:        defaultResponse,
		EnableSummary:          true,
		SendResponse:           false,
		AllowInterimWebPublish: true,
	}
	opts, err := resolveTurnProfileOptions(al.GetConfig(), opts)
	if err != nil {
		return nil, processOptions{}, err
	}
	return agent, opts, nil
}

// maxSenderDisplayNameRunes caps the sender's display name in the prompt.
const maxSenderDisplayNameRunes = 64

// sanitizeSenderDisplayName makes a display name, which the sender chooses,
// safe to quote in the system prompt: line breaks and other spacing become
// single spaces, control and formatting characters (such as bidi overrides)
// are dropped, and it is cut to 64 characters, so a name cannot pass for
// instructions.
func sanitizeSenderDisplayName(name string) string {
	var b strings.Builder
	runes := 0
	space := false
	for _, r := range name {
		if unicode.IsSpace(r) {
			space = b.Len() > 0
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			continue
		}
		if space {
			if runes+1 >= maxSenderDisplayNameRunes {
				break
			}
			b.WriteByte(' ')
			runes++
			space = false
		}
		if runes >= maxSenderDisplayNameRunes {
			break
		}
		b.WriteRune(r)
		runes++
	}
	return b.String()
}

func (al *AgentLoop) resolveMessageRoute(msg bus.InboundMessage) (routing.ResolvedRoute, *AgentInstance, error) {
	registry := al.GetRegistry()
	inboundCtx := normalizedInboundContext(msg)
	route := registry.ResolveRoute(inboundCtx)

	agent, ok := registry.GetAgent(route.AgentID)
	if !ok {
		agent = registry.GetDefaultAgent()
	}
	if agent == nil {
		return routing.ResolvedRoute{}, nil, fmt.Errorf("no agent available for route (agent_id=%s)", route.AgentID)
	}

	return route, agent, nil
}

func (al *AgentLoop) allocateRouteSession(route routing.ResolvedRoute, msg bus.InboundMessage) session.Allocation {
	return session.AllocateRouteSession(session.AllocationInput{
		AgentID:       route.AgentID,
		Context:       normalizedInboundContext(msg),
		SessionPolicy: route.SessionPolicy,
	})
}
