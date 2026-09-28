// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xibodev/compa/pkg/agent/interfaces"
	"github.com/xibodev/compa/pkg/audio/asr"
	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/commands"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/constants"
	runtimeevents "github.com/xibodev/compa/pkg/events"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/media"
	"github.com/xibodev/compa/pkg/providers"
	"github.com/xibodev/compa/pkg/state"
	"github.com/xibodev/compa/pkg/utils"
)

type AgentLoop struct {
	// Core dependencies
	bus      interfaces.MessageBus
	cfg      *config.Config
	registry *AgentRegistry
	state    *state.Manager

	// toolProviders contribute capabilities from OUTSIDE the kernel.
	//
	// Empty is the normal standalone case: a kernel with no providers is a
	// plain conversational runtime. Full Compa injects one backed by the
	// detached-module host; a standalone product injects a native in-process
	// one. See ToolProvider in agent_options.go for why this is an interface
	// rather than a direct call.
	toolProviders []ToolProvider

	// Runtime event system
	runtimeEvents      runtimeevents.Bus
	ownsRuntimeEvents  bool
	runtimeEventLogMu  sync.RWMutex
	runtimeEventLogger *runtimeEventLogger
	runtimeEventLogSub runtimeevents.Subscription
	hooks              *HookManager

	// Runtime state
	running        atomic.Bool
	contextManager ContextManager
	failover       *providers.Failover
	channelManager interfaces.ChannelManager
	mediaStore     media.MediaStore
	transcriber    asr.Transcriber
	cmdRegistry    *commands.Registry
	mcp            mcpRuntime
	evolution      *evolutionBridge
	hookRuntime    hookRuntime
	steering       *steeringQueue
	pendingSkills  sync.Map
	pendingStops   sync.Map
	mu             sync.RWMutex

	// workerSem limits concurrent turn processing workers.
	workerSem chan struct{}

	// activeTurnStates tracks active turns per session to prevent duplicates.
	activeTurnStates sync.Map
	subTurnCounter   atomic.Int64

	turnSeq atomic.Uint64

	// activeReqMu/activeReqCond/activeReqCount replace sync.WaitGroup to
	// avoid the "WaitGroup is reused before previous Wait has returned" panic
	// that occurs when Add(1) races with a goroutine-launched Wait().
	activeReqMu    sync.Mutex
	activeReqCond  *sync.Cond
	activeReqCount int

	reloadFunc func() error

	// provider, when not nil, serves the default model selection instead of
	// a resolution: the seam embedders and tests inject a provider through.
	provider providers.LLMProvider
	// resolveModel resolves every model selection the loop runs on.
	resolveModel ModelResolver
	// rateLimits registers instance RPM limits with failover's limiter;
	// both are replaced together on reload, failover keeping its instance
	// health.
	rateLimits *candidateRateLimits
}

// processOptions configures how a message is processed. Addressing, session
// and message facts live in Dispatch; the remaining fields tune the turn.
type processOptions struct {
	Dispatch                DispatchRequest // Routed request boundary for this turn
	SenderDisplayName       string          // Current sender display name for dynamic context
	ForcedSkills            []string        // Skills explicitly requested for this message
	TurnProfile             config.EffectiveTurnProfile
	SystemPromptOverride    string              // Override the default system prompt (Used by SubTurns)
	InitialSteeringMessages []providers.Message // Steering messages injected before the first LLM call
	DefaultResponse         string              // Response when LLM returns empty
	EnableSummary           bool                // Whether to trigger summarization
	SendResponse            bool                // Whether to send response via bus
	AllowInterimWebPublish  bool                // Whether web tool-call interim text can be published when SendResponse is false
	SuppressToolFeedback    bool                // Whether to suppress inline tool feedback messages
	NoHistory               bool                // If true, don't load session history (for heartbeat)
	SkipInitialSteeringPoll bool                // If true, skip the steering poll at loop start (used by Continue)
	// RequestedSelection is the model selection the message chose, and
	// InstanceResolution what it resolved to; the turn runs on its targets
	// instead of the agent's model.
	RequestedSelection string
	InstanceResolution *providers.InstanceResolution
}

type continuationTarget struct {
	SessionKey string
	Channel    string
	ChatID     string
}

const (
	defaultResponse            = "The model returned an empty response. This may indicate a provider error or token limit."
	toolLimitResponse          = "I've reached `max_tool_iterations` without a final response. Increase `max_tool_iterations` in config.json if this task needs more tool steps."
	handledToolResponseSummary = "Requested output delivered via tool attachment."
	pendingTurnPrefix          = "pending-"
	providerReloadGracePeriod  = 30 * time.Second
	metadataKeyMessageKind     = "message_kind"
	metadataKeyToolCalls       = "tool_calls"
	metadataKeyOutboundKind    = "outbound_kind"
	messageKindThought         = "thought"
	messageKindToolFeedback    = "tool_feedback"
	messageKindToolCalls       = "tool_calls"
	outboundKindFinal          = "final"
	metadataKeyAccountID       = "account_id"
	metadataKeyGuildID         = "guild_id"
	metadataKeyTeamID          = "team_id"
	metadataKeyReplyToMessage  = "reply_to_message_id"
	metadataKeyParentPeerKind  = "parent_peer_kind"
	metadataKeyParentPeerID    = "parent_peer_id"
)

func (al *AgentLoop) Run(ctx context.Context) error {
	al.running.Store(true)

	if err := al.ensureHooksInitialized(ctx); err != nil {
		return err
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		return err
	}

	idleTicker := time.NewTicker(100 * time.Millisecond)
	defer idleTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-idleTicker.C:
			if !al.running.Load() {
				return nil
			}
		case msg, ok := <-al.bus.InboundChan():
			if !ok {
				return nil
			}

			// Resolve the session key for this message
			sessionKey, agentID, ok := al.resolveSteeringTarget(msg)
			if !ok {
				// Non-routable message (e.g., system) — process immediately.
				// Note: system messages are processed in the main goroutine,
				// so they block the receive loop but guarantee session serialization.
				al.processMessageSync(ctx, msg)
				continue
			}

			// Atomically claim the session key with a unique placeholder sentinel
			// to prevent a TOCTOU race where multiple messages for the same session
			// pass the Load check before either registers.
			// The placeholder ensures GetActiveTurnBySession() never returns nil
			// during turn setup. Each placeholder has a unique turnID to prevent
			// cross-worker cleanup issues.
			placeholder := &turnState{
				turnID: makePendingTurnID(sessionKey, al.turnSeq.Add(1)),
				phase:  TurnPhaseSetup,
			}
			if _, loaded := al.activeTurnStates.LoadOrStore(sessionKey, placeholder); loaded {
				if al.tryHandleStopCommand(ctx, msg, sessionKey) {
					continue
				}

				msg = al.prepareInboundMessageForAgent(ctx, msg)

				// Another turn is already active (or reserved) for this session — enqueue
				// it, keeping the model the message selected for a continuation turn.
				if err := al.enqueueSteeringMessage(sessionKey, agentID, providers.Message{
					Role:               "user",
					Content:            msg.Content,
					Media:              append([]string(nil), msg.Media...),
					RequestedSelection: strings.TrimSpace(msg.Context.Raw[bus.MetadataKeyModelSelection]),
				}); err != nil {
					logger.WarnCF("agent", "Failed to enqueue steering message",
						map[string]any{
							"error":       err.Error(),
							"channel":     msg.Channel,
							"chat_id":     msg.ChatID,
							"session_key": sessionKey,
						})
				}
				continue
			}

			// Session claimed — spawn a worker goroutine that acquires a semaphore
			// slot. The goroutine is spawned immediately so the main loop keeps
			// draining the inbound channel. The goroutine blocks on the semaphore.
			go func(m bus.InboundMessage, ph *turnState) {
				var releaseSession bool
				// Acquire semaphore slot (blocks if at capacity)
				select {
				case al.workerSem <- struct{}{}:
					// Got slot, start worker
				case <-ctx.Done():
					// Context canceled while waiting for a slot — clean up the
					// placeholder to prevent session-level deadlock.
					al.releaseSessionTurnState(sessionKey, nil)
					return
				}

				// Safety-net cleanup: if the placeholder was never replaced by a real
				// turnState (e.g., error before runTurn), delete it here. When runTurn
				// completes normally, clearActiveTurn deletes the real turnState and
				// this becomes a no-op (the key is already gone).
				defer func() {
					if releaseSession {
						// Conditional delete: only remove the entry if it still points
						// to our placeholder. A new message may have claimed the slot
						// between the panic and this defer.
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

				if al.channelManager != nil {
					defer al.channelManager.InvokeTypingStop(m.Channel, m.ChatID)
				}

				if al.takePendingStop(sessionKey) {
					al.releaseSessionTurnState(sessionKey, nil)
					target := &continuationTarget{
						SessionKey: sessionKey,
						Channel:    m.Channel,
						ChatID:     m.ChatID,
					}
					continued, continueErr := al.drainQueuedSteeringContinuations(ctx, target)
					if continueErr != nil {
						al.maybePublishError(ctx, m.Channel, m.ChatID, sessionKey, continueErr)
						return
					}
					if continued != "" {
						al.PublishResponseIfNeeded(ctx, target.Channel, target.ChatID, target.SessionKey, continued)
					}
					return
				}

				al.runTurnWithSteering(ctx, m)
			}(msg, placeholder)

			// TODO: Re-enable media cleanup after inbound media is properly consumed by the agent.
			// Currently disabled because files are deleted before the LLM can access their content.
			// defer func() {
			// 	if al.mediaStore != nil && msg.MediaScope != "" {
			// 		if releaseErr := al.mediaStore.ReleaseAll(msg.MediaScope); releaseErr != nil {
			// 			logger.WarnCF("agent", "Failed to release media", map[string]any{
			// 				"scope": msg.MediaScope,
			// 				"error": releaseErr.Error(),
			// 			})
			// 		}
			// 	}
			// }()
		}
	}
}

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

// Close releases resources held by agent session stores. Call after Stop.
func (al *AgentLoop) Close() {
	mcpManager := al.mcp.takeManager()

	if mcpManager != nil {
		if err := mcpManager.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close MCP manager",
				map[string]any{
					"error": err.Error(),
				})
		}
	}
	evolution := al.currentEvolutionBridge()
	if evolution != nil {
		if err := evolution.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close evolution bridge",
				map[string]any{
					"error": err.Error(),
				})
		}
	}

	// The context manager can own a resource -- the seahorse one holds an open
	// SQLite handle. Leaving it open leaked a database connection on every
	// shutdown, which Windows makes visible (it refuses to unlink an open file)
	// and Linux hides entirely.
	//
	// Not every manager holds one: the default manager is a view over the loop
	// and has nothing to release, so this asks rather than requires.
	if closer, ok := al.contextManager.(interface{ Close() error }); ok && closer != nil {
		if err := closer.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close context manager",
				map[string]any{
					"error": err.Error(),
				})
		}
	}

	al.GetRegistry().Close()
	if al.hooks != nil {
		al.hooks.Close()
	}
	al.closeRuntimeEventLogger()
	if al.runtimeEvents != nil && al.ownsRuntimeEvents {
		if err := al.runtimeEvents.Close(); err != nil {
			logger.ErrorCF("agent", "Failed to close runtime event bus",
				map[string]any{
					"error": err.Error(),
				})
		}
	}
}

type turnEventScope struct {
	agentID    string
	sessionKey string
	turnID     string
	context    *TurnContext
}

// ReloadProviderAndConfig atomically swaps the config and rebuilds every
// agent from it: each agent resolves its model selection anew, so a changed
// default model, route or provider instance takes effect. provider, when not
// nil, serves the default selection instead of a resolution, as in
// NewAgentLoop. It uses a context to allow timeout control from the caller.
// Returns an error if the reload fails or context is canceled.
func (al *AgentLoop) ReloadProviderAndConfig(
	ctx context.Context,
	provider providers.LLMProvider,
	cfg *config.Config,
) error {
	if cfg == nil {
		return fmt.Errorf("config cannot be nil")
	}

	var registry *AgentRegistry
	func() {
		defer func() {
			if r := recover(); r != nil {
				logger.RecoverPanicNoExit(r)
				logger.ErrorCF("agent", "Panic during registry creation",
					map[string]any{"panic": r})
				registry = nil
			}
		}()
		registry = NewAgentRegistry(cfg, provider, al.modelResolver())
	}()
	if registry == nil {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("context canceled during registry creation: %w", err)
		}
		return fmt.Errorf("registry creation failed")
	}

	// Check context again before proceeding
	if err := ctx.Err(); err != nil {
		closeUnreferencedStatefulProviders(registry.providerMap(), al.GetRegistry().providerMap())
		return fmt.Errorf("context canceled after registry creation: %w", err)
	}

	// Ensure shared tools are re-registered on the new registry
	registerSharedTools(al, cfg, al.bus, registry)

	newEvolution, evolutionErr := newEvolutionBridge(registry, cfg, registryModelProvider(registry))
	if evolutionErr != nil {
		logger.WarnCF("agent", "Failed to reinitialize evolution bridge during reload",
			map[string]any{"error": evolutionErr.Error()})
	}
	if newEvolution != nil {
		newEvolution.setCurrentCheck(al.isCurrentEvolutionBridge)
		if err := newEvolution.subscribeRuntimeEvents(al.runtimeEvents.Channel()); err != nil {
			logger.WarnCF("agent", "Failed to subscribe reloaded evolution bridge to runtime events",
				map[string]any{"error": err.Error()})
		}
	}

	// Rate limits start over with the new config's candidates.
	rateLimits := newCandidateRateLimits()
	rateLimits.register(registry.candidates())

	// Atomically swap the config and registry under write lock
	// This ensures readers see a consistent pair
	al.mu.Lock()
	oldRegistry := al.registry
	oldEvolution := al.evolution

	// Store new values
	al.cfg = cfg
	al.provider = provider
	al.registry = registry
	al.evolution = newEvolution
	al.rateLimits = rateLimits
	al.failover = al.failover.WithLimits(rateLimits.registry)

	al.mu.Unlock()
	al.refreshRuntimeEventLogger(cfg)

	oldMCPManager := al.mcp.reset()
	al.hookRuntime.reset(al)
	configureHookManagerFromConfig(al.hooks, cfg)
	if err := al.ensureHooksInitialized(ctx); err != nil {
		logger.WarnCF("agent", "Configured hooks failed to reinitialize after reload",
			map[string]any{"error": err.Error()})
	}
	if oldMCPManager != nil {
		if err := oldMCPManager.Close(); err != nil {
			logger.WarnCF("agent", "Failed to close previous MCP manager during reload",
				map[string]any{"error": err.Error()})
		}
	}
	if oldEvolution != nil {
		if err := oldEvolution.Close(); err != nil {
			logger.WarnCF("agent", "Failed to close previous evolution bridge during reload",
				map[string]any{"error": err.Error()})
		}
	}
	if err := al.ensureMCPInitialized(ctx); err != nil {
		logger.WarnCF("agent", "MCP failed to reinitialize after reload",
			map[string]any{"error": err.Error()})
	}

	// Close the previous agents' providers the new ones do not share, after
	// releasing the lock (so readers are not blocked) and once in-flight
	// requests drain.
	al.closeReloadedProviders(ctx, oldRegistry.providerMap(), registry.providerMap())

	fields := map[string]any{"model": cfg.Agents.Defaults.GetModelName()}
	if agent := registry.GetDefaultAgent(); agent != nil && !agent.hasModel() {
		fields["model_error"] = agent.noModelError().Error()
	}
	logger.InfoCF("agent", "Provider and config reloaded successfully", fields)

	return nil
}

var audioAnnotationRe = regexp.MustCompile(`\[(voice|audio)(?::[^\]]*)?\]`)

// runAgentLoop remains the top-level shell that starts a turn and publishes
// any post-turn work. runTurn owns the full turn lifecycle.
func (al *AgentLoop) runAgentLoop(
	ctx context.Context,
	agent *AgentInstance,
	opts processOptions,
) (string, error) {
	opts, err := resolveTurnProfileOptions(al.GetConfig(), opts)
	if err != nil {
		return "", err
	}

	// Record last channel for heartbeat notifications (skip internal channels and cli)
	if opts.Dispatch.Channel() != "" &&
		opts.Dispatch.ChatID() != "" &&
		!constants.IsInternalChannel(opts.Dispatch.Channel()) {
		channelKey := fmt.Sprintf("%s:%s", opts.Dispatch.Channel(), opts.Dispatch.ChatID())
		if recordErr := al.RecordLastChannel(channelKey); recordErr != nil {
			logger.WarnCF(
				"agent",
				"Failed to record last channel",
				map[string]any{"error": recordErr.Error()},
			)
		}
	}

	ensureSessionMetadata(agent.Sessions, opts.Dispatch.SessionKey, opts.Dispatch.SessionScope)

	turnScope := al.newTurnEventScope(
		agent.ID,
		opts.Dispatch.SessionKey,
		newTurnContext(opts.Dispatch.InboundContext, opts.Dispatch.RouteResult, opts.Dispatch.SessionScope),
	)
	ts := newTurnState(agent, opts, turnScope)
	pipeline := NewPipeline(al)
	result, err := al.runTurn(ctx, ts, pipeline)
	if err != nil {
		return "", err
	}
	if result.status == TurnEndStatusAborted {
		return "", nil
	}

	for _, followUp := range result.followUps {
		if pubErr := al.bus.PublishInbound(ctx, followUp); pubErr != nil {
			logger.WarnCF("agent", "Failed to publish follow-up after turn",
				map[string]any{
					"turn_id": ts.turnID,
					"error":   pubErr.Error(),
				})
		}
	}

	if opts.SendResponse && result.finalContent != "" {
		agentID, sessionKey, scope := outboundTurnMetadata(
			agent.ID,
			opts.Dispatch.SessionKey,
			opts.Dispatch.SessionScope,
		)
		msg := bus.OutboundMessage{
			Context: outboundContextFromInbound(
				opts.Dispatch.InboundContext,
				opts.Dispatch.Channel(),
				opts.Dispatch.ChatID(),
				opts.Dispatch.ReplyToMessageID(),
			),
			AgentID:      agentID,
			SessionKey:   sessionKey,
			Scope:        scope,
			Content:      result.finalContent,
			ContextUsage: computeContextUsage(agent, opts.Dispatch.SessionKey),
		}
		if modelName := strings.TrimSpace(result.modelName); modelName != "" {
			if msg.Context.Raw == nil {
				msg.Context.Raw = make(map[string]string, 1)
			}
			msg.Context.Raw["model_name"] = modelName
		}
		if opts.InstanceResolution != nil {
			for key, value := range instanceSelectionResultMetadata(result) {
				if msg.Context.Raw == nil {
					msg.Context.Raw = make(map[string]string, 3)
				}
				msg.Context.Raw[key] = value
			}
		}
		markFinalOutbound(&msg)
		al.bus.PublishOutbound(ctx, msg)
	}

	if result.finalContent != "" {
		responsePreview := utils.Truncate(result.finalContent, 120)
		logger.InfoCF("agent", fmt.Sprintf("Response: %s", responsePreview),
			map[string]any{
				"agent_id":     agent.ID,
				"session_key":  opts.Dispatch.SessionKey,
				"iterations":   ts.currentIteration(),
				"final_length": len(result.finalContent),
			})
	}

	return result.finalContent, nil
}
