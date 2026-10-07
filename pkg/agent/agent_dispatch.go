// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/tools"
)

// defaultMaxParallelTurns is how many turns run at once when
// agents.defaults.max_parallel_turns is unset. Turns of one session never
// run concurrently: what arrives for a busy session waits for its turn.
const defaultMaxParallelTurns = 4

// reservedSystemChannel was the channel async tool results came back on.
// Nothing publishes on it any more, and nothing is answered on it.
const reservedSystemChannel = "system"

// dispatchInbound hands msg to its session's turn. It runs on the receive
// loop and never waits on the network: turns, transcription and replies run
// in goroutines of their own.
func (al *AgentLoop) dispatchInbound(ctx context.Context, msg bus.InboundMessage) {
	if msg.Channel == reservedSystemChannel || msg.Context.Channel == reservedSystemChannel {
		logger.WarnCF("agent", "Dropped an inbound message on the reserved system channel",
			map[string]any{"sender_id": msg.SenderID})
		return
	}

	sessionKey, agentID, ok := al.resolveSteeringTarget(msg)
	if !ok {
		// No agent takes the message: a worker tells the chat why.
		go al.runWorker(ctx, func() { al.processMessageSync(ctx, msg) })
		return
	}

	// The owner's answer to an approval request acts at once: the turn
	// waiting for it may hold this session, or every worker.
	if al.handleApprovalReply(ctx, msg, sessionKey) {
		return
	}

	if al.intake.busy(sessionKey) {
		// An earlier message of this session is still being transcribed:
		// this one waits behind it, keeping the session's order.
		al.queueIntake(ctx, msg, sessionKey, agentID)
		return
	}
	al.claimOrSteer(ctx, msg, sessionKey, agentID, false)
}

// claimOrSteer starts a turn for msg when its session is idle. Otherwise msg
// goes to the running turn: as a command meant for that moment, or as
// steering. prepared reports whether msg was already transcribed.
func (al *AgentLoop) claimOrSteer(
	ctx context.Context,
	msg bus.InboundMessage,
	sessionKey, agentID string,
	prepared bool,
) {
	// Atomically claim the session key with a unique placeholder sentinel to
	// prevent a TOCTOU race where multiple messages for the same session pass
	// the Load check before either registers. The placeholder ensures
	// GetActiveTurnBySession() never returns nil during turn setup, and its
	// unique turnID prevents cross-worker cleanup issues.
	placeholder := &turnState{
		turnID: makePendingTurnID(sessionKey, al.turnSeq.Add(1)),
		phase:  TurnPhaseSetup,
	}
	placeholder.opts.Dispatch.InboundContext = cloneInboundContext(&msg.Context)
	if _, loaded := al.activeTurnStates.LoadOrStore(sessionKey, placeholder); !loaded {
		go al.runSessionWorker(ctx, msg, sessionKey, placeholder)
		return
	}

	// Commands meant for this moment act now; the rest of what arrives joins
	// the running turn as steering.
	if al.dispatchCommandDuringTurn(ctx, msg, sessionKey) {
		return
	}
	if !prepared && al.needsTranscription(msg) {
		// Transcription calls a speech service: it runs off the receive loop.
		al.queueIntake(ctx, msg, sessionKey, agentID)
		return
	}
	if !prepared {
		msg = bus.NormalizeInboundMessage(msg)
	}

	if al.steersAnotherSender(sessionKey, msg) {
		// Someone else's message is not part of the running turn: it waits
		// for a turn of its own, under its own sender.
		if err := al.deferTurn(sessionKey, msg); err != nil {
			al.reportSteeringFailure(ctx, msg, sessionKey, err)
		}
		return
	}

	// Another turn is already active (or reserved) for this session: enqueue
	// msg, keeping the model it selected for a continuation turn.
	if err := al.enqueueSteeringMessage(sessionKey, agentID, providers.Message{
		Role:               "user",
		Content:            msg.Content,
		Media:              append([]string(nil), msg.Media...),
		RequestedSelection: strings.TrimSpace(msg.Context.Raw[bus.MetadataKeyModelSelection]),
	}); err != nil {
		al.reportSteeringFailure(ctx, msg, sessionKey, err)
	}
}

func (al *AgentLoop) reportSteeringFailure(ctx context.Context, msg bus.InboundMessage, sessionKey string, err error) {
	logger.WarnCF("agent", "Failed to enqueue steering message",
		map[string]any{
			"error":       err.Error(),
			"channel":     msg.Channel,
			"chat_id":     msg.ChatID,
			"session_key": sessionKey,
		})
	if errors.Is(err, errSteeringQueueFull) {
		go al.publishDirectReply(ctx, msg, sessionKey, steeringQueueFullReply)
	}
}

// runWorker runs fn in a worker slot: at most max_parallel_turns run at once.
func (al *AgentLoop) runWorker(ctx context.Context, fn func()) bool {
	select {
	case al.workerSem <- struct{}{}:
	case <-ctx.Done():
		return false
	}
	defer func() { <-al.workerSem }()
	defer func() {
		if r := recover(); r != nil {
			logger.RecoverPanicNoExit(r)
			logger.ErrorCF("agent", "Worker goroutine panicked", map[string]any{"panic": fmt.Sprintf("%v", r)})
		}
	}()
	fn()
	return true
}

// runSessionWorker runs the turn of m, whose session ph holds, once a worker
// slot is free, and then whatever queued up for the session meanwhile.
func (al *AgentLoop) runSessionWorker(ctx context.Context, m bus.InboundMessage, sessionKey string, ph *turnState) {
	var releaseSession bool
	// Acquire a worker slot (blocks while every slot is busy).
	select {
	case al.workerSem <- struct{}{}:
	case <-ctx.Done():
		// Canceled while waiting for a slot: release the placeholder so the
		// session does not stay claimed.
		al.releaseSessionTurnState(sessionKey, nil)
		return
	}

	// Safety-net cleanup: if the placeholder was never replaced by a real
	// turnState (e.g., error before runTurn), delete it here. When runTurn
	// completes normally, clearActiveTurn deletes the real turnState and
	// this becomes a no-op (the key is already gone).
	defer func() {
		if releaseSession {
			// Conditional delete: only remove the entry if it still points to
			// our placeholder. A new message may have claimed the slot between
			// the panic and this defer.
			if actual, ok := al.activeTurnStates.Load(sessionKey); ok {
				if ts, ok := actual.(*turnState); ok && ts == ph {
					al.releaseSessionTurnState(sessionKey, ts)
				}
			}
			return
		}
		if actual, ok := al.activeTurnStates.Load(sessionKey); ok {
			if ts, ok := actual.(*turnState); ok && strings.HasPrefix(ts.turnID, pendingTurnPrefix) {
				// Placeholder still present — runTurn never replaced it.
				al.releaseSessionTurnState(sessionKey, ts)
			}
		}
	}()

	defer func() {
		if r := recover(); r != nil {
			releaseSession = true
			logger.RecoverPanicNoExit(r)
			logger.ErrorCF("agent", "Worker goroutine panicked",
				map[string]any{
					"session_key": sessionKey,
					"channel":     m.Channel,
					"chat_id":     m.ChatID,
					"panic":       fmt.Sprintf("%v", r),
				})
		}
	}()
	defer func() { <-al.workerSem }() // Release slot

	if cm := al.currentChannelManager(); cm != nil {
		defer cm.InvokeTypingStop(m.Channel, m.ChatID)
	}

	if al.takePendingStop(sessionKey) {
		al.releaseSessionTurnState(sessionKey, nil)
		target := al.continuationTargetFor(m, sessionKey, "")
		if err := al.drainQueuedSteeringContinuations(ctx, target); err != nil {
			al.maybePublishErrorTo(ctx, target, err)
		}
		return
	}

	al.runTurnWithSteering(ctx, m)
	al.runDeferredTurns(ctx, sessionKey)
}

// needsTranscription reports whether msg carries media a configured
// transcriber may have to turn into text.
func (al *AgentLoop) needsTranscription(msg bus.InboundMessage) bool {
	return al.transcriber != nil && al.currentMediaStore() != nil && len(msg.Media) > 0
}

// sessionIntake orders the messages of a session that wait, off the receive
// loop, for transcription, together with those that arrive behind them.
type sessionIntake struct {
	mu    sync.Mutex
	tails map[string]chan struct{}
}

func (in *sessionIntake) busy(sessionKey string) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	_, ok := in.tails[sessionKey]
	return ok
}

// queueIntake prepares msg (transcribing its audio) in a goroutine, after
// the session's earlier messages in intake, and then dispatches it.
func (al *AgentLoop) queueIntake(ctx context.Context, msg bus.InboundMessage, sessionKey, agentID string) {
	al.intake.mu.Lock()
	if al.intake.tails == nil {
		al.intake.tails = make(map[string]chan struct{})
	}
	previous := al.intake.tails[sessionKey]
	done := make(chan struct{})
	al.intake.tails[sessionKey] = done
	al.intake.mu.Unlock()

	go func() {
		defer func() {
			al.intake.mu.Lock()
			if al.intake.tails[sessionKey] == done {
				delete(al.intake.tails, sessionKey)
			}
			al.intake.mu.Unlock()
			close(done)
		}()
		defer func() {
			if r := recover(); r != nil {
				logger.RecoverPanicNoExit(r)
			}
		}()
		if previous != nil {
			select {
			case <-previous:
			case <-ctx.Done():
				return
			}
		}
		prepared := al.prepareInboundMessageForAgent(ctx, msg)
		al.claimOrSteer(ctx, prepared, sessionKey, agentID, true)
	}()
}

// steersAnotherSender reports whether msg comes from someone other than the
// sender of the turn running in sessionKey, as in a group chat.
func (al *AgentLoop) steersAnotherSender(sessionKey string, msg bus.InboundMessage) bool {
	sender := strings.TrimSpace(msg.Context.SenderID)
	if sender == "" {
		sender = strings.TrimSpace(msg.SenderID)
	}
	if sender == "" {
		return false
	}
	ts := al.getActiveTurnState(sessionKey)
	if ts == nil {
		return false
	}
	ts.mu.RLock()
	active := strings.TrimSpace(ts.opts.Dispatch.SenderID())
	ts.mu.RUnlock()
	return active != "" && active != sender
}

// deferredTurns holds, per session, messages of other senders that arrived
// during a turn: each runs as a turn of its own once the session is free.
type deferredTurns struct {
	mu     sync.Mutex
	queues map[string][]bus.InboundMessage
}

func (al *AgentLoop) deferTurn(sessionKey string, msg bus.InboundMessage) error {
	al.deferred.mu.Lock()
	defer al.deferred.mu.Unlock()
	if al.deferred.queues == nil {
		al.deferred.queues = make(map[string][]bus.InboundMessage)
	}
	if len(al.deferred.queues[sessionKey]) >= MaxQueueSize {
		return errSteeringQueueFull
	}
	al.deferred.queues[sessionKey] = append(al.deferred.queues[sessionKey], msg)
	return nil
}

func (al *AgentLoop) takeDeferredTurn(sessionKey string) (bus.InboundMessage, bool) {
	al.deferred.mu.Lock()
	defer al.deferred.mu.Unlock()
	queue := al.deferred.queues[sessionKey]
	if len(queue) == 0 {
		return bus.InboundMessage{}, false
	}
	msg := queue[0]
	if len(queue) == 1 {
		delete(al.deferred.queues, sessionKey)
	} else {
		al.deferred.queues[sessionKey] = queue[1:]
	}
	return msg, true
}

// clearDeferredTurns drops the turns waiting for sessionKey (/stop).
func (al *AgentLoop) clearDeferredTurns(sessionKey string) int {
	al.deferred.mu.Lock()
	defer al.deferred.mu.Unlock()
	count := len(al.deferred.queues[sessionKey])
	delete(al.deferred.queues, sessionKey)
	return count
}

// runDeferredTurns runs, one after another, the turns other senders' messages
// queued for sessionKey while it was busy. A turn that finds the session
// claimed again leaves the rest to the turn that claimed it.
func (al *AgentLoop) runDeferredTurns(ctx context.Context, sessionKey string) {
	for ctx.Err() == nil {
		msg, ok := al.takeDeferredTurn(sessionKey)
		if !ok {
			return
		}
		placeholder := &turnState{
			turnID: makePendingTurnID(sessionKey, al.turnSeq.Add(1)),
			phase:  TurnPhaseSetup,
		}
		placeholder.opts.Dispatch.InboundContext = cloneInboundContext(&msg.Context)
		if _, loaded := al.activeTurnStates.LoadOrStore(sessionKey, placeholder); loaded {
			// The session's next turn runs what is left.
			al.requeueDeferredTurn(sessionKey, msg)
			return
		}
		func() {
			defer al.releaseSessionTurnState(sessionKey, placeholder)
			al.runTurnWithSteering(ctx, msg)
		}()
	}
}

func (al *AgentLoop) requeueDeferredTurn(sessionKey string, msg bus.InboundMessage) {
	al.deferred.mu.Lock()
	defer al.deferred.mu.Unlock()
	if al.deferred.queues == nil {
		al.deferred.queues = make(map[string][]bus.InboundMessage)
	}
	al.deferred.queues[sessionKey] = append([]bus.InboundMessage{msg}, al.deferred.queues[sessionKey]...)
}

// sessionWorkContexts holds, per session, the context the sub-agents of its
// turns run under. /stop cancels it, stopping them whether or not the turn
// that started them still runs.
type sessionWorkContexts struct {
	mu      sync.Mutex
	entries map[string]*sessionWork
}

type sessionWork struct {
	ctx    context.Context
	cancel context.CancelFunc
	refs   int
}

// acquire returns the context of sessionKey's sub-agents, derived from
// parent, and the function to call when the sub-agent ends.
func (s *sessionWorkContexts) acquire(parent context.Context, sessionKey string) (context.Context, func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries == nil {
		s.entries = make(map[string]*sessionWork)
	}
	work := s.entries[sessionKey]
	if work == nil || work.ctx.Err() != nil {
		ctx, cancel := context.WithCancel(parent)
		work = &sessionWork{ctx: ctx, cancel: cancel}
		s.entries[sessionKey] = work
	}
	work.refs++
	var once sync.Once
	return work.ctx, func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			work.refs--
			if work.refs > 0 {
				return
			}
			work.cancel()
			if s.entries[sessionKey] == work {
				delete(s.entries, sessionKey)
			}
		})
	}
}

// cancel stops the sub-agents running for sessionKey and reports how many
// there were.
func (s *sessionWorkContexts) cancel(sessionKey string) int {
	s.mu.Lock()
	work := s.entries[sessionKey]
	delete(s.entries, sessionKey)
	running := 0
	if work != nil {
		running = work.refs
	}
	s.mu.Unlock()
	if work != nil {
		work.cancel()
	}
	return running
}

// rootTurn returns the root turn ts belongs to.
func rootTurn(ts *turnState) *turnState {
	for ts != nil && ts.parentTurnState != nil {
		ts = ts.parentTurnState
	}
	return ts
}

// asyncToolOrigin is the turn an async tool call came from: its result is
// processed for that session and agent.
type asyncToolOrigin struct {
	ts        *turnState
	agentID   string
	session   string
	depth     int
	iteration int
	tool      string
	scheduled bool
	target    *continuationTarget
}

// asyncToolCallback returns the callback through which an async tool, such
// as spawn, reports its result, possibly after the turn that called it ended.
func (al *AgentLoop) asyncToolCallback(ctx context.Context, ts *turnState, toolName string, iteration int) tools.AsyncCallback {
	origin := asyncToolOrigin{
		ts:        ts,
		agentID:   ts.agent.ID,
		session:   ts.sessionKey,
		depth:     ts.depth,
		iteration: iteration,
		tool:      toolName,
		scheduled: isScheduledTurn(ctx),
		target: &continuationTarget{
			SessionKey: ts.sessionKey,
			Channel:    ts.channel,
			ChatID:     ts.chatID,
			AgentID:    ts.agent.ID,
			Inbound:    cloneInboundContext(ts.opts.Dispatch.InboundContext),
		},
	}
	return func(_ context.Context, result *tools.ToolResult) {
		al.handleAsyncToolResult(origin, result)
	}
}

// handleAsyncToolResult processes an async tool's result as a turn of the
// session and agent that called the tool. The user sees the reply to it, not
// the raw result as well.
func (al *AgentLoop) handleAsyncToolResult(origin asyncToolOrigin, result *tools.ToolResult) {
	if result == nil {
		return
	}
	if errors.Is(result.Err, context.Canceled) {
		// Stopped with /stop: there is nothing to report.
		logger.InfoCF("agent", "Async tool stopped", map[string]any{"tool": origin.tool, "session_key": origin.session})
		return
	}
	content := result.ContentForLLM()
	if content == "" {
		if !result.Silent && result.ForUser != "" {
			outCtx, outCancel := context.WithTimeout(al.lifetimeContext(), 5*time.Second)
			defer outCancel()
			_ = al.bus.PublishOutbound(outCtx, outboundMessageForTurn(origin.ts, result.ForUser))
		}
		return
	}
	if origin.depth > 0 {
		// A sub-turn hears from its own sub-agents through its pending
		// results; its session ends with it.
		return
	}

	content = al.GetConfig().FilterSensitiveData(content)
	logger.InfoCF("agent", "Async tool completed; continuing its session",
		map[string]any{
			"tool":        origin.tool,
			"content_len": len(content),
			"channel":     origin.target.Channel,
			"session_key": origin.session,
		})
	al.emitEvent(
		runtimeevents.KindAgentFollowUpQueued,
		origin.ts.scope.meta(origin.iteration, "runTurn", "turn.follow_up.queued"),
		FollowUpQueuedPayload{
			SourceTool: origin.tool,
			ContentLen: len(content),
		},
	)

	if err := al.enqueueSteeringMessage(origin.session, origin.agentID,
		asyncToolResultPromptMessage(origin.tool, content)); err != nil {
		logger.WarnCF("agent", "Failed to queue an async tool result",
			map[string]any{"tool": origin.tool, "session_key": origin.session, "error": err.Error()})
		return
	}
	go al.runAsyncResultTurn(origin)
}

// asyncResultRetry bounds how long an async result waits for its session to
// be claimed when the session's running turn is just ending.
const (
	asyncResultRetries    = 20
	asyncResultRetryDelay = 250 * time.Millisecond
)

// runAsyncResultTurn runs the queued async result as a turn of its session in
// a worker. When a turn of the session runs, that turn takes it.
func (al *AgentLoop) runAsyncResultTurn(origin asyncToolOrigin) {
	ctx := al.lifetimeContext()
	if origin.scheduled {
		ctx = withScheduledTurn(ctx)
	}
	al.runWorker(ctx, func() {
		for attempt := 0; attempt < asyncResultRetries; attempt++ {
			if al.pendingSteeringCountForScope(origin.session) == 0 {
				return
			}
			err := al.drainQueuedSteeringContinuations(ctx, origin.target)
			if errors.Is(err, errSessionBusy) {
				// The session's turn takes the result, unless it is just
				// ending: check again shortly.
				select {
				case <-time.After(asyncResultRetryDelay):
					continue
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				al.maybePublishErrorTo(ctx, origin.target, err)
			}
			return
		}
	})
}

// asyncToolResultPromptMessage presents an async tool's result to the model
// as the tool's output, not as something the user wrote.
func asyncToolResultPromptMessage(toolName, content string) providers.Message {
	return promptMessageWithMetadata(
		providers.Message{
			Role: "user",
			Content: fmt.Sprintf(
				"[Tool output: result of the background %s call, not a message from the user]\n%s",
				toolName, content),
		},
		PromptLayerTurn,
		PromptSlotToolResult,
		PromptSourceToolResult,
	)
}
