package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/xibodev/compa/v4/pkg/bus"
	runtimeevents "github.com/xibodev/compa/v4/pkg/events"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/providers"
	"github.com/xibodev/compa/v4/pkg/session"
)

// SteeringMode controls how queued steering messages are dequeued.
type SteeringMode string

const (
	// SteeringOneAtATime dequeues only the first queued message per poll.
	SteeringOneAtATime SteeringMode = "one-at-a-time"
	// SteeringAll drains the entire queue in a single poll.
	SteeringAll SteeringMode = "all"
	// MaxQueueSize number of possible messages in the Steering Queue
	MaxQueueSize = 10
)

// errSteeringQueueFull is returned when a session already has MaxQueueSize
// messages waiting for its running turn.
var errSteeringQueueFull = errors.New("steering queue is full")

// parseSteeringMode normalizes a config string into a SteeringMode.
func parseSteeringMode(s string) SteeringMode {
	switch s {
	case "all":
		return SteeringAll
	default:
		return SteeringOneAtATime
	}
}

// steeringQueue is a thread-safe set of per-session queues of user messages
// that can be injected into a running agent loop between tool calls. Each
// queue is keyed by the session key of the turn it steers.
type steeringQueue struct {
	mu     sync.Mutex
	queues map[string][]providers.Message
	mode   SteeringMode
}

func newSteeringQueue(mode SteeringMode) *steeringQueue {
	return &steeringQueue{
		queues: make(map[string][]providers.Message),
		mode:   mode,
	}
}

// pushScope enqueues a steering message for the provided session scope.
func (sq *steeringQueue) pushScope(scope string, msg providers.Message) error {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return fmt.Errorf("steering scope is required")
	}

	sq.mu.Lock()
	defer sq.mu.Unlock()

	queue := sq.queues[scope]
	if len(queue) >= MaxQueueSize {
		return errSteeringQueueFull
	}
	sq.queues[scope] = append(queue, msg)
	return nil
}

// dequeueScope removes and returns pending steering messages for the provided
// scope according to the configured mode.
func (sq *steeringQueue) dequeueScope(scope string) []providers.Message {
	sq.mu.Lock()
	defer sq.mu.Unlock()

	scope = strings.TrimSpace(scope)
	queue := sq.queues[scope]
	if len(queue) == 0 {
		return nil
	}

	switch sq.mode {
	case SteeringAll:
		msgs := append([]providers.Message(nil), queue...)
		delete(sq.queues, scope)
		return msgs
	default:
		msg := queue[0]
		queue[0] = providers.Message{} // Clear reference for GC
		queue = queue[1:]
		if len(queue) == 0 {
			delete(sq.queues, scope)
		} else {
			sq.queues[scope] = queue
		}
		return []providers.Message{msg}
	}
}

// lenScope returns the number of queued messages for a specific scope.
func (sq *steeringQueue) lenScope(scope string) int {
	sq.mu.Lock()
	defer sq.mu.Unlock()
	return len(sq.queues[strings.TrimSpace(scope)])
}

func (sq *steeringQueue) clearScope(scope string) int {
	sq.mu.Lock()
	defer sq.mu.Unlock()

	scope = strings.TrimSpace(scope)
	count := len(sq.queues[scope])
	if count > 0 {
		delete(sq.queues, scope)
	}
	return count
}

// enqueueSteeringMessage queues a user message for the turn running in the
// given session. The message is injected after the current tool finishes,
// skipping any remaining tool calls in the batch.
func (al *AgentLoop) enqueueSteeringMessage(scope, agentID string, msg providers.Message) error {
	if al.steering == nil {
		return fmt.Errorf("steering queue is not initialized")
	}

	msg = steeringPromptMessage(msg)
	if err := al.steering.pushScope(scope, msg); err != nil {
		logger.WarnCF("agent", "Failed to enqueue steering message", map[string]any{
			"error": err.Error(),
			"role":  msg.Role,
			"scope": scope,
		})
		return err
	}

	queueDepth := al.steering.lenScope(scope)
	logger.DebugCF("agent", "Steering message enqueued", map[string]any{
		"role":        msg.Role,
		"content_len": len(msg.Content),
		"media_count": len(msg.Media),
		"queue_len":   queueDepth,
		"scope":       scope,
	})

	meta := HookMeta{
		AgentID:    agentID,
		SessionKey: scope,
		Source:     "Steer",
		TracePath:  "turn.interrupt.received",
	}
	if ts := al.getActiveTurnState(scope); ts != nil && !strings.HasPrefix(ts.turnID, pendingTurnPrefix) {
		meta = ts.eventMeta("Steer", "turn.interrupt.received")
	}

	al.emitEvent(
		runtimeevents.KindAgentInterruptReceived,
		meta,
		InterruptReceivedPayload{
			Kind:       InterruptKindSteering,
			Role:       msg.Role,
			ContentLen: len(msg.Content),
			QueueDepth: queueDepth,
		},
	)

	return nil
}

func (al *AgentLoop) dequeueSteeringMessagesForScope(scope string) []providers.Message {
	if al.steering == nil {
		return nil
	}
	return al.steering.dequeueScope(scope)
}

func (al *AgentLoop) pendingSteeringCountForScope(scope string) int {
	if al.steering == nil {
		return 0
	}
	return al.steering.lenScope(scope)
}

func (al *AgentLoop) clearSteeringMessagesForScope(scope string) int {
	if al.steering == nil {
		return 0
	}
	return al.steering.clearScope(scope)
}

func (al *AgentLoop) continueWithSteeringMessages(
	ctx context.Context,
	agent *AgentInstance,
	target *continuationTarget,
	scope *session.SessionScope,
	steeringMsgs []providers.Message,
) (string, error) {
	dispatch := DispatchRequest{
		SessionKey:   target.SessionKey,
		SessionScope: session.CloneScope(scope),
	}
	switch {
	case target.Inbound != nil:
		// The continuation answers the same chat as the turn before it.
		dispatch.InboundContext = cloneInboundContext(target.Inbound)
	case target.Channel != "" || target.ChatID != "":
		dispatch.InboundContext = &bus.InboundContext{
			Channel:  target.Channel,
			ChatID:   target.ChatID,
			ChatType: inferChatTypeFromSessionScope(scope),
		}
	}
	// The caller publishes the reply, as for a message's own turn
	// (messageTurnOptions); interim output, such as an answer that more
	// steering overtakes, goes out as it comes.
	opts := processOptions{
		Dispatch:                dispatch,
		DefaultResponse:         defaultResponse,
		EnableSummary:           true,
		SendResponse:            false,
		AllowInterimWebPublish:  true,
		InitialSteeringMessages: steeringMsgs,
		SkipInitialSteeringPoll: true,
	}
	// The continuation runs on the model the queued messages selected, as
	// they would have run had they arrived while the session was idle.
	if selection := lastRequestedSelection(steeringMsgs); selection != "" {
		resolved, err := al.resolveTurnSelection(selection)
		if err != nil {
			return "", fmt.Errorf("model selection %q is not available: %w", selection, err)
		}
		opts.RequestedSelection = selection
		opts.InstanceResolution = resolved
	}
	return al.runAgentLoop(ctx, agent, opts)
}

// lastRequestedSelection returns the model selection of the latest message
// that made one.
func lastRequestedSelection(messages []providers.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if selection := strings.TrimSpace(messages[i].RequestedSelection); selection != "" {
			return selection
		}
	}
	return ""
}

func (al *AgentLoop) agentForSession(sessionKey string) *AgentInstance {
	registry := al.GetRegistry()
	if registry == nil {
		return nil
	}

	agentIDs := registry.ListAgentIDs()
	sort.Strings(agentIDs)
	for _, agentID := range agentIDs {
		agent, ok := registry.GetAgent(agentID)
		if !ok || agent == nil {
			continue
		}
		resolvedAgentID := session.ResolveAgentID(agent.Sessions, sessionKey)
		if resolvedAgentID == "" {
			continue
		}
		if scopedAgent, ok := registry.GetAgent(resolvedAgentID); ok {
			return scopedAgent
		}
	}

	return registry.GetDefaultAgent()
}

// errSessionBusy is returned by Continue when a turn of the session runs: it
// takes the queued messages itself.
var errSessionBusy = errors.New("a turn of the session is running")

// Continue resumes an idle agent by dequeuing any pending steering messages
// and running them through the agent loop. This is used when the agent's last
// message was from the assistant (i.e., it has stopped processing) and the
// user has since enqueued steering messages.
//
// If no steering messages are pending, it returns an empty string.
func (al *AgentLoop) Continue(ctx context.Context, sessionKey, channel, chatID string) (string, error) {
	return al.continueTarget(ctx, &continuationTarget{SessionKey: sessionKey, Channel: channel, ChatID: chatID})
}

func (al *AgentLoop) continueTarget(ctx context.Context, target *continuationTarget) (string, error) {
	sessionKey := target.SessionKey
	// Claim the session with a unique placeholder to prevent a TOCTOU race where two
	// concurrent Continue calls for the same session both pass the active-turn
	// check and create parallel turns. The placeholder is replaced by the real
	// turnState inside continueWithSteeringMessages → runAgentLoop → registerActiveTurn.
	placeholder := &turnState{
		turnID: "pending-continue-" + sessionKey + "-" + fmt.Sprintf("%d", al.turnSeq.Add(1)),
		phase:  TurnPhaseSetup,
	}
	placeholder.opts.Dispatch.InboundContext = cloneInboundContext(target.Inbound)
	if _, loaded := al.activeTurnStates.LoadOrStore(sessionKey, placeholder); loaded {
		if active := al.GetActiveTurnBySession(sessionKey); active != nil {
			return "", fmt.Errorf("turn %s is still active for session %q: %w", active.TurnID, sessionKey, errSessionBusy)
		}
		// Another Continue just claimed the slot; let it handle the steering.
		return "", nil
	}
	// The real turn replaces the placeholder; anything else releases it.
	defer al.releaseSessionTurnState(sessionKey, placeholder)

	al.prepareExtensions(ctx)

	steeringMsgs := al.dequeueSteeringMessagesForScope(sessionKey)
	if len(steeringMsgs) == 0 {
		return "", nil
	}

	var agent *AgentInstance
	if target.AgentID != "" {
		agent, _ = al.GetRegistry().GetAgent(target.AgentID)
	}
	if agent == nil {
		agent = al.agentForSession(sessionKey)
	}
	if agent == nil {
		return "", fmt.Errorf("no agent available for session %q", sessionKey)
	}

	if tool, ok := agent.Tools.Get("message"); ok {
		if resetter, ok := tool.(interface{ ResetSentInRound(sessionKey string) }); ok {
			resetter.ResetSentInRound(sessionKey)
		}
	}

	var scope *session.SessionScope
	if metaStore, ok := agent.Sessions.(session.MetadataAwareSessionStore); ok {
		scope = metaStore.GetSessionScope(sessionKey)
	}

	return al.continueWithSteeringMessages(ctx, agent, target, scope, steeringMsgs)
}

// ====================== Hard Abort ======================

// HardAbort immediately cancels the running agent loop for the given session,
// cascading the cancellation to all child SubTurns. This is a destructive operation
// that terminates execution without waiting for graceful cleanup.
//
// Use this when the user explicitly requests immediate termination (e.g., "stop now", "abort").
// To redirect a running turn instead, deliver another inbound message for the
// session: the agent loop queues it as steering for the active turn.
func (al *AgentLoop) HardAbort(sessionKey string) error {
	tsInterface, ok := al.activeTurnStates.Load(sessionKey)
	if !ok {
		return fmt.Errorf("no active turn state found for session %s", sessionKey)
	}

	ts, ok := tsInterface.(*turnState)
	if !ok {
		return fmt.Errorf("invalid turn state type for session %s", sessionKey)
	}

	if strings.HasPrefix(ts.turnID, "pending-") {
		return fmt.Errorf("turn is still initializing for session %s", sessionKey)
	}

	logger.InfoCF("agent", "Hard abort triggered", map[string]any{
		"session_key":            sessionKey,
		"turn_id":                ts.turnID,
		"depth":                  ts.depth,
		"initial_history_length": ts.initialHistoryLength,
	})

	// Cancel the active provider/tool turn contexts immediately so long-running
	// execution stops as soon as possible on the root turn.
	_ = ts.requestHardAbort()

	// IMPORTANT: Trigger cascading cancellation FIRST to stop all child SubTurns
	// from adding more messages to the session. This prevents race conditions
	// where rollback happens while children are still writing.
	// Use isHardAbort=true for hard abort to immediately cancel all children.
	ts.Finish(true)

	// Roll back session history to the state before the turn started.
	if ts.session != nil {
		history := ts.session.GetHistory(sessionKey)
		if ts.initialHistoryLength < len(history) {
			ts.session.SetHistory(sessionKey, history[:ts.initialHistoryLength])
		}
	}

	return nil
}
