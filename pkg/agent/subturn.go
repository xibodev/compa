package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/providers/messageutil"
	"github.com/xibodev/compa/v3/pkg/tools"
)

// ====================== Config & Constants ======================
const (
	// Default values for SubTurn configuration (used when config is not set or is zero)
	defaultMaxSubTurnDepth       = 3
	defaultMaxConcurrentSubTurns = 5
	defaultConcurrencyTimeout    = 30 * time.Second
	defaultSubTurnTimeout        = 5 * time.Minute
	// maxEphemeralHistorySize limits the number of messages stored in ephemeral sessions.
	// This prevents memory accumulation in long-running sub-turns.
	maxEphemeralHistorySize = 50
)

var (
	ErrDepthLimitExceeded = errors.New("sub-turn depth limit exceeded")
	ErrConcurrencyTimeout = errors.New("timeout waiting for concurrency slot")
)

// getSubTurnConfig returns the effective SubTurn configuration with defaults applied.
func (al *AgentLoop) getSubTurnConfig() subTurnRuntimeConfig {
	var cfg config.SubTurnConfig
	if loopCfg := al.GetConfig(); loopCfg != nil {
		cfg = loopCfg.Agents.Defaults.SubTurn
	}

	maxDepth := cfg.MaxDepth
	if maxDepth <= 0 {
		maxDepth = defaultMaxSubTurnDepth
	}

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = defaultMaxConcurrentSubTurns
	}

	concurrencyTimeout := time.Duration(cfg.ConcurrencyTimeoutSec) * time.Second
	if concurrencyTimeout <= 0 {
		concurrencyTimeout = defaultConcurrencyTimeout
	}

	defaultTimeout := time.Duration(cfg.DefaultTimeoutMinutes) * time.Minute
	if defaultTimeout <= 0 {
		defaultTimeout = defaultSubTurnTimeout
	}

	return subTurnRuntimeConfig{
		maxDepth:           maxDepth,
		maxConcurrent:      maxConcurrent,
		concurrencyTimeout: concurrencyTimeout,
		defaultTimeout:     defaultTimeout,
	}
}

// subTurnRuntimeConfig holds the effective runtime configuration for SubTurn execution.
type subTurnRuntimeConfig struct {
	maxDepth           int
	maxConcurrent      int
	concurrencyTimeout time.Duration
	defaultTimeout     time.Duration
}

// ====================== SubTurn Config ======================

// SubTurnConfig configures the execution of a child sub-turn. The child runs
// on its agent's model — or, running as its parent's agent, on the model the
// parent's message selected.
//
// Usage Examples:
//
// Synchronous sub-turn (Async=false):
//
//	cfg := SubTurnConfig{
//	    SystemPrompt: "Analyze this code",
//	    Async: false,  // Result returned immediately
//	}
//	result, err := SpawnSubTurn(ctx, cfg)
//	// Use result directly here
//	processResult(result)
//
// Asynchronous sub-turn (Async=true):
//
//	cfg := SubTurnConfig{
//	    SystemPrompt: "Background analysis",
//	    Async: true,  // Result delivered to channel
//	}
//	result, err := SpawnSubTurn(ctx, cfg)
//	// Result also available in parent's pendingResults channel
//	// Parent turn will poll and process it in a later iteration
type SubTurnConfig struct {
	// SystemPrompt, despite its name, carries the task description: it is
	// sent as the first 'user' message (see ActualSystemPrompt).
	SystemPrompt string

	// Async controls the result delivery mechanism:
	//
	// When Async = false (synchronous sub-turn):
	//   - The caller blocks until the sub-turn completes
	//   - The result is ONLY returned via the function return value
	//   - The result is NOT delivered to the parent's pendingResults channel
	//   - This prevents double delivery: caller gets result immediately, no need for channel
	//   - Use case: When the caller needs the result immediately to continue execution
	//   - Example: A tool that needs to process the sub-turn result before returning
	//
	// When Async = true (asynchronous sub-turn):
	//   - The sub-turn runs in the background (still blocks the caller, but semantically async)
	//   - The result is delivered to the parent's pendingResults channel
	//   - The result is ALSO returned via the function return value (for consistency)
	//   - The parent turn can poll pendingResults in later iterations to process results
	//   - Use case: Fire-and-forget operations, or when results are processed in batches
	//   - Example: Spawning multiple sub-turns in parallel and collecting results later
	//
	// IMPORTANT: The Async flag does NOT make the call non-blocking. It only controls
	// whether the result is delivered via the channel. For true non-blocking execution,
	// the caller must spawn the sub-turn in a separate goroutine.
	Async bool

	// Critical indicates this SubTurn's result is important and should continue
	// running even after the parent turn finishes gracefully.
	//
	// When parent finishes gracefully (Finish(false)):
	//   - Critical=true: SubTurn continues running, delivers result as orphan
	//   - Critical=false: SubTurn exits gracefully without error
	//
	// When parent finishes with hard abort (Finish(true)):
	//   - All SubTurns are canceled regardless of Critical flag
	Critical bool

	// Timeout is the maximum duration for this SubTurn.
	// If the SubTurn runs longer than this, it will be canceled.
	// Default is 5 minutes (defaultSubTurnTimeout) if not specified.
	Timeout time.Duration

	// ActualSystemPrompt is injected as the true 'system' role message for the childAgent.
	// SystemPrompt, despite its name, carries the task description and is sent as
	// the first 'user' message.
	ActualSystemPrompt string

	// InitialMessages preloads the ephemeral session history before the agent loop starts.
	// Used by evaluator-optimizer patterns to pass the full worker context across multiple iterations.
	InitialMessages []providers.Message

	// TargetAgentID, when set, runs the sub-turn as the specified agent.
	// The target agent's workspace, model, tools, and system prompt are used
	// instead of the caller's. If empty, the sub-turn runs as the parent agent.
	TargetAgentID string
}

// ====================== Context Keys ======================
type agentLoopKeyType struct{}

var agentLoopKey = agentLoopKeyType{}

// WithAgentLoop injects AgentLoop into context for tool access
func WithAgentLoop(ctx context.Context, al *AgentLoop) context.Context {
	return context.WithValue(ctx, agentLoopKey, al)
}

// AgentLoopFromContext retrieves AgentLoop from context
func AgentLoopFromContext(ctx context.Context) *AgentLoop {
	al, _ := ctx.Value(agentLoopKey).(*AgentLoop)
	return al
}

// ====================== Helper Functions ======================

func (al *AgentLoop) generateSubTurnID() string {
	return fmt.Sprintf("subturn-%d", al.subTurnCounter.Add(1))
}

// ====================== Core Function: spawnSubTurn ======================

// AgentLoopSpawner implements tools.SubTurnSpawner interface.
// This allows tools to spawn sub-turns without circular dependency.
type AgentLoopSpawner struct {
	al *AgentLoop
}

// SpawnSubTurn implements tools.SubTurnSpawner interface.
func (s *AgentLoopSpawner) SpawnSubTurn(
	ctx context.Context,
	cfg tools.SubTurnConfig,
) (*tools.ToolResult, error) {
	parentTS := turnStateFromContext(ctx)
	if parentTS == nil {
		return nil, errors.New(
			"parent turnState not found in context - cannot spawn sub-turn outside of a turn",
		)
	}

	// Convert tools.SubTurnConfig to agent.SubTurnConfig. The child runs as an
	// agent, with that agent's own tools and model settings.
	agentCfg := SubTurnConfig{
		SystemPrompt:       cfg.SystemPrompt,
		ActualSystemPrompt: cfg.ActualSystemPrompt,
		InitialMessages:    cfg.InitialMessages,
		Async:              cfg.Async,
		Critical:           cfg.Critical,
		Timeout:            cfg.Timeout,
		TargetAgentID:      cfg.TargetAgentID,
	}

	return spawnSubTurn(ctx, s.al, parentTS, agentCfg)
}

// NewSubTurnSpawner creates a SubTurnSpawner for the given AgentLoop.
func NewSubTurnSpawner(al *AgentLoop) *AgentLoopSpawner {
	return &AgentLoopSpawner{al: al}
}

// SpawnSubTurn is the exported entry point for tools to spawn sub-turns.
// It retrieves AgentLoop and parent turnState from context and delegates to spawnSubTurn.
func SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*tools.ToolResult, error) {
	al := AgentLoopFromContext(ctx)
	if al == nil {
		return nil, errors.New(
			"AgentLoop not found in context - ensure context is properly initialized",
		)
	}

	parentTS := turnStateFromContext(ctx)
	if parentTS == nil {
		return nil, errors.New(
			"parent turnState not found in context - cannot spawn sub-turn outside of a turn",
		)
	}

	return spawnSubTurn(ctx, al, parentTS, cfg)
}

func spawnSubTurn(
	ctx context.Context,
	al *AgentLoop,
	parentTS *turnState,
	cfg SubTurnConfig,
) (result *tools.ToolResult, err error) {
	// Get effective SubTurn configuration
	rtCfg := al.getSubTurnConfig()

	// Sub-agents run under their root session's work context, which /stop
	// cancels whether or not the turn that started them still runs.
	workCtx, releaseWork := al.sessionWork.acquire(al.lifetimeContext(), rootTurn(parentTS).sessionKey)
	defer releaseWork()
	// An async child outlives the tool call that started it: only /stop or
	// its own timeout end it. A sync child ends with its caller as well.
	waitCtx := workCtx
	if !cfg.Async {
		waitCtx = ctx
	}

	// 0. Acquire concurrency semaphore FIRST to ensure it's released even if early validation fails.
	// Blocks if parent already has maxConcurrentSubTurns running, with a timeout to prevent indefinite blocking.
	// Also respects context cancellation so we don't block forever if parent is aborted.
	// NOTE: The semaphore is released immediately after runTurn completes (not in a defer) to
	// ensure it is freed before the cleanup phase (async result delivery), which may block on
	// a full pendingResults channel. Holding the semaphore through cleanup would allow the
	// parent's goroutine to be blocked waiting for a semaphore slot while child turns are
	// blocked delivering results — a deadlock.
	var semAcquired bool
	if parentTS.concurrencySem != nil {
		// Create a timeout context for semaphore acquisition
		timeoutCtx, cancel := context.WithTimeout(waitCtx, rtCfg.concurrencyTimeout)
		defer cancel()

		select {
		case parentTS.concurrencySem <- struct{}{}:
			semAcquired = true
			defer func() {
				if semAcquired {
					<-parentTS.concurrencySem
				}
			}()
		case <-workCtx.Done():
			return nil, workCtx.Err()
		case <-timeoutCtx.Done():
			// Check parent context first - if it was canceled, propagate that error
			if waitCtx.Err() != nil {
				return nil, waitCtx.Err()
			}
			// Otherwise it's our timeout
			return nil, fmt.Errorf("%w: all %d slots occupied for %v",
				ErrConcurrencyTimeout, rtCfg.maxConcurrent, rtCfg.concurrencyTimeout)
		}
	}

	// 1. Depth limit check
	if parentTS.depth >= rtCfg.maxDepth {
		logger.WarnCF("subturn", "Depth limit exceeded", map[string]any{
			"parent_id": parentTS.turnID,
			"depth":     parentTS.depth,
			"max_depth": rtCfg.maxDepth,
		})
		return nil, ErrDepthLimitExceeded
	}

	// 2. Determine timeout for child SubTurn
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = rtCfg.defaultTimeout
	}

	// 3. Derive the child context from the session's work context, not from
	// the parent turn's: an async child may continue after its parent turn
	// ends, while /stop still reaches it. The child has its own timeout.
	childCtx, cancel := context.WithTimeout(workCtx, timeout)
	defer cancel()
	if !cfg.Async {
		// The caller waits for a sync child: when the caller stops, so does it.
		stopWithCaller := context.AfterFunc(ctx, cancel)
		defer stopWithCaller()
	}
	// A scheduled turn's children are scheduled work too: nobody wrote what
	// they do (see turnSenderIsOwner).
	if isScheduledTurn(ctx) {
		childCtx = withScheduledTurn(childCtx)
	}

	childID := al.generateSubTurnID()

	// Resolve the agent instance for the child turn.
	// When TargetAgentID is set, look up that agent from the registry so the
	// child runs with the target's workspace, model, tools, and system prompt.
	// Otherwise fall back to the parent's agent (existing behavior).
	var baseAgent *AgentInstance
	if cfg.TargetAgentID != "" {
		var ok bool
		baseAgent, ok = al.GetRegistry().GetAgent(cfg.TargetAgentID)
		if !ok {
			return nil, fmt.Errorf("target agent %q not found in registry", cfg.TargetAgentID)
		}
	} else {
		baseAgent = parentTS.agent
		if baseAgent == nil {
			baseAgent = al.GetRegistry().GetDefaultAgent()
		}
	}
	if baseAgent == nil {
		return nil, errors.New("parent turnState has no agent instance")
	}
	ephemeralStore := newEphemeralSession(nil)
	// The child runs on its agent's model as it is now, like any turn: for
	// the parent's agent, the model the parent runs on. An async child that
	// starts after its parent ended, past a /switch that closed that model,
	// takes the agent's current model instead.
	snapshot, releaseModel, current := baseAgent.turnSnapshot()
	if !current {
		if live, ok := al.GetRegistry().GetAgent(baseAgent.ID); ok && live != nil {
			snapshot, releaseModel, _ = live.turnSnapshot()
		}
	}
	defer releaseModel()
	agent := *snapshot
	agent.Sessions = ephemeralStore
	// Clone the tool registry so child turn's tool registrations
	// don't pollute the parent's registry.
	if baseAgent.Tools != nil {
		agent.Tools = baseAgent.Tools.Clone()
	}

	// Create processOptions for the child turn
	dispatch := DispatchRequest{
		SessionKey:     childID,
		UserMessage:    cfg.SystemPrompt,
		Media:          nil,
		InboundContext: cloneInboundContext(parentTS.opts.Dispatch.InboundContext),
	}
	opts := processOptions{
		Dispatch:                dispatch,
		SenderDisplayName:       parentTS.opts.SenderDisplayName,
		TurnProfile:             parentTS.profile,
		SystemPromptOverride:    cfg.ActualSystemPrompt,
		InitialSteeringMessages: cfg.InitialMessages,
		DefaultResponse:         "",
		EnableSummary:           false,
		SendResponse:            false,
		NoHistory:               true, // SubTurns don't use session history
		SkipInitialSteeringPoll: true,
	}
	if !opts.TurnProfile.Enabled {
		opts.TurnProfile = parentTS.opts.TurnProfile
	}
	// A child runs on the model its parent's message selected, if it
	// selected one, when it runs as the parent's agent — or as a delegated
	// agent that has no model of its own. It resolves the selection itself
	// and owns the providers it builds: sharing the parent's resolution would
	// leave open what only the child built, and an async child may outlive
	// the parent that closes its providers.
	if selection := parentTS.opts.RequestedSelection; selection != "" && parentTS.opts.InstanceResolution != nil {
		_, _, targetModelErr := baseAgent.primaryModel()
		if cfg.TargetAgentID == "" || targetModelErr != nil {
			resolved, resolveErr := al.resolveTurnSelection(selection)
			if resolveErr != nil {
				return nil, fmt.Errorf("model selection %q is not available: %w", selection, resolveErr)
			}
			opts.RequestedSelection = selection
			opts.InstanceResolution = resolved
		}
	}

	// Create event scope for the child turn
	scope := al.newTurnEventScope(
		agent.ID,
		childID,
		newTurnContext(opts.Dispatch.InboundContext, opts.Dispatch.RouteResult, opts.Dispatch.SessionScope),
	)

	// Create child turnState using the new API
	childTS := newTurnState(&agent, opts, scope)

	// Set SubTurn-specific fields
	childTS.cancelFunc = cancel
	childTS.critical = cfg.Critical
	childTS.depth = parentTS.depth + 1
	childTS.parentTurnID = parentTS.turnID
	childTS.parentTurnState = parentTS
	childTS.pendingResults = make(chan *tools.ToolResult, 16)
	childTS.concurrencySem = make(chan struct{}, rtCfg.maxConcurrent)
	childTS.al = al                  // back-ref for hard abort cascade
	childTS.session = ephemeralStore // same store as agent.Sessions
	// The approval policy decides a child's calls as its parent's.
	childTS.origin = parentTS.origin

	// IMPORTANT: Put childTS into childCtx so that code inside runTurn can retrieve it
	childCtx = withTurnState(childCtx, childTS)
	childCtx = WithAgentLoop(childCtx, al) // Propagate AgentLoop to child turn

	childTS.ctx = childCtx

	// Register child turn state so GetAllActiveTurns/Subagents can find it
	al.activeTurnStates.Store(childID, childTS)
	defer al.activeTurnStates.Delete(childID)

	// 4. Establish parent-child relationship (thread-safe)
	parentTS.mu.Lock()
	parentTS.childTurnIDs = append(parentTS.childTurnIDs, childID)
	parentTS.mu.Unlock()

	// 5. Emit Spawn event
	al.emitEvent(runtimeevents.KindAgentSubTurnSpawn,
		childTS.eventMeta("spawnSubTurn", "subturn.spawn"),
		SubTurnSpawnPayload{
			AgentID:      childTS.agentID,
			Label:        childID,
			ParentTurnID: parentTS.turnID,
		},
	)

	// 6. Defer cleanup: deliver result (for async), emit End event, and recover from panics
	defer func() {
		if r := recover(); r != nil {
			logger.RecoverPanicNoExit(r)
			err = fmt.Errorf("subturn panicked: %v", r)
			result = nil
			logger.ErrorCF("subturn", "SubTurn panicked", map[string]any{
				"child_id":  childID,
				"parent_id": parentTS.turnID,
				"panic":     r,
			})
		}

		// Result Delivery Strategy (Async vs Sync)
		if cfg.Async {
			deliverSubTurnResult(al, parentTS, childID, result)
		}

		status := "completed"
		if err != nil {
			status = "error"
		}
		al.emitEvent(runtimeevents.KindAgentSubTurnEnd,
			childTS.eventMeta("spawnSubTurn", "subturn.end"),
			SubTurnEndPayload{
				AgentID: childTS.agentID,
				Status:  status,
			},
		)
	}()

	// 7. Execute sub-turn via the real agent loop.
	pipeline := NewPipeline(al)
	turnRes, turnErr := al.runTurn(childCtx, childTS, pipeline)

	// Release the concurrency semaphore immediately after runTurn completes,
	// before the cleanup defer runs. This prevents a deadlock where:
	// - All semaphore slots are held by sub-turns in their cleanup phase
	// - Cleanup blocks on a full pendingResults channel
	// - The parent goroutine is blocked waiting for a semaphore slot
	// - The parent cannot consume pendingResults because it is blocked on the semaphore
	if semAcquired {
		<-parentTS.concurrencySem
		semAcquired = false // prevent the defer from double-releasing
	}

	// Convert turnResult to tools.ToolResult
	if turnErr != nil {
		err = turnErr
		result = &tools.ToolResult{
			Err:    turnErr,
			ForLLM: fmt.Sprintf("SubTurn failed: %v", turnErr),
		}
	} else {
		result = &tools.ToolResult{
			ForLLM:  turnRes.finalContent,
			ForUser: turnRes.finalContent,
		}
	}

	return result, err
}

// ====================== Result Delivery ======================

// deliverSubTurnResult delivers a sub-turn result to the parent turn's pendingResults channel.
//
// IMPORTANT: This function is ONLY called for asynchronous sub-turns (Async=true).
// For synchronous sub-turns (Async=false), results are returned directly via the function
// return value to avoid double delivery.
//
// Delivery behavior:
//   - If parent turn is still running: attempts to deliver to pendingResults channel
//   - If channel is full: emits agent.subturn.orphan (result is lost from channel but tracked)
//   - If parent turn has finished: emits agent.subturn.orphan (late arrival)
//
// Thread safety:
//   - Reads parent state under lock, then releases lock before channel send
//   - Small race window exists but is acceptable (worst case: result becomes orphan)
//
// Event emissions:
//   - agent.subturn.result_delivered: successful delivery to channel
//   - agent.subturn.orphan: delivery failed (parent finished or channel full)
func deliverSubTurnResult(al *AgentLoop, parentTS *turnState, childID string, result *tools.ToolResult) {
	// Let GC clean up the pendingResults channel; parent Finish will no longer close it.
	// We use defer/recover to catch any unlikely channel panics if it were ever closed.
	defer func() {
		if r := recover(); r != nil {
			logger.RecoverPanicNoExit(r)
			logger.WarnCF("subturn", "recovered panic sending to pendingResults", map[string]any{
				"parent_id": parentTS.turnID,
				"child_id":  childID,
				"recover":   r,
			})
			if result != nil && al != nil {
				al.emitEvent(runtimeevents.KindAgentSubTurnOrphan,
					parentTS.eventMeta("deliverSubTurnResult", "subturn.orphan"),
					SubTurnOrphanPayload{ParentTurnID: parentTS.turnID, ChildTurnID: childID, Reason: "panic"},
				)
			}
		}
	}()
	parentTS.mu.Lock()
	isFinished := parentTS.isFinished.Load()
	resultChan := parentTS.pendingResults
	parentTS.mu.Unlock()

	// If parent turn has already finished, treat this as an orphan result
	if isFinished || resultChan == nil {
		if result != nil && al != nil {
			al.emitEvent(runtimeevents.KindAgentSubTurnOrphan,
				parentTS.eventMeta("deliverSubTurnResult", "subturn.orphan"),
				SubTurnOrphanPayload{ParentTurnID: parentTS.turnID, ChildTurnID: childID, Reason: "parent_finished"},
			)
		}
		return
	}

	// Parent Turn is still running → attempt to deliver result
	// We use a select statement with parentTS.Finished() to ensure that if the
	// parent turn finishes while we are waiting to send the result (e.g. channel
	// is full), we don't leak this goroutine by blocking forever.
	select {
	case resultChan <- result:
		// Successfully delivered; a sub-turn that panicked delivers nil.
		if al != nil {
			contentLen := 0
			if result != nil {
				contentLen = len(result.ForLLM)
			}
			al.emitEvent(runtimeevents.KindAgentSubTurnResultDelivered,
				parentTS.eventMeta("deliverSubTurnResult", "subturn.result_delivered"),
				SubTurnResultDeliveredPayload{ContentLen: contentLen},
			)
		}
	case <-parentTS.Finished():
		// Parent finished while we were waiting to deliver.
		// The result cannot be delivered to the LLM, so it becomes an orphan.
		logger.WarnCF("subturn", "parent finished before result could be delivered", map[string]any{
			"parent_id": parentTS.turnID,
			"child_id":  childID,
		})
		if result != nil && al != nil {
			al.emitEvent(
				runtimeevents.KindAgentSubTurnOrphan,
				parentTS.eventMeta("deliverSubTurnResult", "subturn.orphan"),
				SubTurnOrphanPayload{
					ParentTurnID: parentTS.turnID,
					ChildTurnID:  childID,
					Reason:       "parent_finished_waiting",
				},
			)
		}
	}
}

// ====================== Other Types ======================

// ephemeralSessionStore is an in-memory session.SessionStore used by SubTurns.
// It does not persist to disk and auto-truncates history to maxEphemeralHistorySize.
type ephemeralSessionStore struct {
	mu      sync.Mutex
	history []providers.Message
	summary string
}

func newEphemeralSession(initial []providers.Message) ephemeralSessionStoreIface {
	s := &ephemeralSessionStore{}
	if len(initial) > 0 {
		s.history = append(s.history, initial...)
	}
	return s
}

// ephemeralSessionStoreIface is satisfied by *ephemeralSessionStore.
// Declared so newEphemeralSession can return a typed interface.
type ephemeralSessionStoreIface interface {
	AddMessage(sessionKey, role, content string)
	AddFullMessage(sessionKey string, msg providers.Message)
	GetHistory(key string) []providers.Message
	GetSummary(key string) string
	SetSummary(key, summary string)
	SetHistory(key string, history []providers.Message)
	TruncateHistory(key string, keepLast int)
	Save(key string) error
	ListSessions() []string
	Close() error
}

func (e *ephemeralSessionStore) AddMessage(_, role, content string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, providers.Message{Role: role, Content: content})
	e.truncateLocked()
}

func (e *ephemeralSessionStore) AddFullMessage(_ string, msg providers.Message) {
	if messageutil.IsTransientAssistantThoughtMessage(msg) {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.history = append(e.history, msg)
	e.truncateLocked()
}

func (e *ephemeralSessionStore) GetHistory(_ string) []providers.Message {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]providers.Message, len(e.history))
	copy(out, e.history)
	return out
}

func (e *ephemeralSessionStore) GetSummary(_ string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.summary
}

func (e *ephemeralSessionStore) SetSummary(_, summary string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.summary = summary
}

func (e *ephemeralSessionStore) SetHistory(_ string, history []providers.Message) {
	e.mu.Lock()
	defer e.mu.Unlock()
	history = messageutil.FilterInvalidHistoryMessages(history)
	e.history = make([]providers.Message, len(history))
	copy(e.history, history)
	e.truncateLocked()
}

func (e *ephemeralSessionStore) TruncateHistory(_ string, keepLast int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if keepLast <= 0 {
		e.history = nil
		return
	}

	if keepLast >= len(e.history) {
		return
	}
	e.history = e.history[len(e.history)-keepLast:]
}

func (e *ephemeralSessionStore) Save(_ string) error    { return nil }
func (e *ephemeralSessionStore) Close() error           { return nil }
func (e *ephemeralSessionStore) ListSessions() []string { return nil }

func (e *ephemeralSessionStore) truncateLocked() {
	if len(e.history) > maxEphemeralHistorySize {
		e.history = e.history[len(e.history)-maxEphemeralHistorySize:]
	}
}
