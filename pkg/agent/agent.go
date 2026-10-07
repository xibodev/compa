// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xibodev/compa/v3/pkg/agent/interfaces"
	"github.com/xibodev/compa/v3/pkg/audio/asr"
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/commands"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/constants"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/media"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/state"
	"github.com/xibodev/compa/v3/pkg/utils"
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
	// intake orders a session's messages waiting for transcription.
	intake sessionIntake
	// deferred holds other senders' messages waiting for a session's turn.
	deferred deferredTurns
	// sessionWork holds each session's context for the sub-agents its turns
	// start: /stop cancels it.
	sessionWork sessionWorkContexts

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

	// lifetime is the context of work the loop starts on its own, such as
	// connecting MCP servers: it outlives any caller's context, and Close
	// ends it.
	lifetime    context.Context
	endLifetime context.CancelFunc

	// approvals holds the tool calls waiting for the owner's /approve.
	approvals *ownerApprovals
	// approvalSeq numbers the asks of the approval policy, for the ids of
	// their approval events.
	approvalSeq atomic.Uint64
	// terminalChat is set when the terminal shows what the loop posts in its
	// chat (SetTerminalChat).
	terminalChat atomic.Bool
	// closingReplaced counts what reloads replaced and is still to close
	// (closeReplaced). Close waits for it.
	closingReplaced sync.WaitGroup
}

// lifetimeContext returns the loop's own context (see lifetime).
func (al *AgentLoop) lifetimeContext() context.Context {
	if al == nil || al.lifetime == nil {
		return context.Background()
	}
	return al.lifetime
}

// reportInitError logs a hook or MCP startup failure.
func (al *AgentLoop) reportInitError(what string, err error) {
	if err == nil {
		return
	}
	logger.ErrorCF("agent", what+" failed to start; continuing without them",
		map[string]any{"error": err.Error()})
}

// initializeExtensions starts the configured hooks and, in the background,
// the MCP servers. Neither failure stops the loop: a hook or server that
// fails is logged (see reportInitError) and left out -- an approval hook
// fails closed -- and MCP tools appear once their servers connect.
func (al *AgentLoop) initializeExtensions(ctx context.Context) {
	_ = al.ensureHooksInitialized(ctx)
	al.startMCPInitialization()
}

// prepareExtensions makes sure hooks are mounted and MCP servers connected
// before a turn that does not come through Run, such as a CLI or cron turn.
// Failures were logged when they happened and don't stop the turn.
func (al *AgentLoop) prepareExtensions(ctx context.Context) {
	_ = al.ensureHooksInitialized(ctx)
	_ = al.ensureMCPInitialized(ctx)
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
	// AgentID is the agent the session's turns run as, when known.
	AgentID string
	// Inbound is the context of the message the session's turn answers:
	// replies keep its account, topic and the message they answer.
	Inbound *bus.InboundContext
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

// Run processes inbound messages until ctx ends or Stop is called, and then
// returns nil. It returns an error only when it cannot process messages at
// all. Hooks and MCP servers that fail to start do not stop it: their
// failures are logged.
func (al *AgentLoop) Run(ctx context.Context) error {
	if al.bus == nil {
		return errors.New("agent loop has no message bus")
	}
	al.running.Store(true)

	al.initializeExtensions(ctx)

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
			al.dispatchInbound(ctx, msg)
		}
	}
}

func (al *AgentLoop) Stop() {
	al.running.Store(false)
}

// Close releases resources held by agent session stores. Call after Stop.
func (al *AgentLoop) Close() {
	// Ending the lifetime aborts MCP servers still connecting, and the new
	// generation makes such a connection close what it started.
	if al.endLifetime != nil {
		al.endLifetime()
	}
	// Once the lifetime ended, what a reload replaced closes without waiting
	// for the requests in flight.
	al.closingReplaced.Wait()
	mcpManager := al.mcp.reset()

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
	closeContextManager(al.currentContextManager())

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
	oldCfg := al.cfg

	// Store new values
	al.cfg = cfg
	al.provider = provider
	al.registry = registry
	al.evolution = newEvolution
	al.rateLimits = rateLimits
	al.failover = al.failover.WithLimits(rateLimits.registry)

	al.mu.Unlock()
	al.refreshRuntimeEventLogger(cfg)
	// The new agents need the context manager's tools, and changed context
	// manager settings a new manager.
	replacedContextManager := al.reloadContextManager(oldCfg, cfg)

	oldMCPManager := al.mcp.reset()
	al.hookRuntime.reset(al)
	configureHookManagerFromConfig(al.hooks, cfg)
	_ = al.ensureHooksInitialized(ctx)
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
	// The new config's MCP servers connect in the background, on the loop's
	// lifetime: ctx ends as soon as the reload returns.
	al.startMCPInitialization()

	// Turns that started before the reload still run on the previous agents'
	// providers and the replaced context manager: those close once their
	// requests end, without the reload waiting for them.
	al.closeReplaced(oldRegistry.providerMap(), registry.providerMap(), replacedContextManager)

	fields := map[string]any{"model": cfg.Agents.Defaults.GetModelName()}
	if agent := registry.GetDefaultAgent(); agent != nil && !agent.hasModel() {
		fields["model_error"] = agent.noModelError().Error()
	}
	logger.InfoCF("agent", "Provider and config reloaded successfully", fields)

	return nil
}

// audioAnnotationRe matches the annotations channels write for audio:
// [audio], [audio: file.ogg] and [voice]. A transcript, [voice: what was
// said], is not one: transcribing again must not replace it.
var audioAnnotationRe = regexp.MustCompile(`\[audio(?::[^\]]*)?\]|\[voice\]`)

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
	al.recordOwnerChat(ctx, opts.Dispatch.InboundContext)

	ensureSessionMetadata(agent.Sessions, opts.Dispatch.SessionKey, opts.Dispatch.SessionScope)

	// The turn runs on the agent's model as it is now; a /switch model takes
	// effect on the next turn.
	agent, releaseModel, _ := agent.turnSnapshot()
	defer releaseModel()

	turnScope := al.newTurnEventScope(
		agent.ID,
		opts.Dispatch.SessionKey,
		newTurnContext(opts.Dispatch.InboundContext, opts.Dispatch.RouteResult, opts.Dispatch.SessionScope),
	)
	ts := newTurnState(agent, opts, turnScope)
	// The turn's sub-agents run at most max_concurrent at once, and a hard
	// abort of the turn stops them (see Finish).
	ts.concurrencySem = make(chan struct{}, al.getSubTurnConfig().maxConcurrent)
	ts.al = al
	pipeline := NewPipeline(al)
	result, err := al.runTurn(ctx, ts, pipeline)
	if err != nil {
		return "", err
	}
	if result.status == TurnEndStatusAborted {
		return "", nil
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
		// Replies hold what people read: INFO records only their size.
		logger.InfoCF("agent", "Response ready",
			map[string]any{
				"agent_id":     agent.ID,
				"session_key":  opts.Dispatch.SessionKey,
				"iterations":   ts.currentIteration(),
				"final_length": len(result.finalContent),
			})
		logger.DebugCF("agent", "Response preview",
			map[string]any{"preview": utils.Truncate(result.finalContent, 120)})
	}

	return result.finalContent, nil
}
