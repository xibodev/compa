// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/xibodev/compa/v4/pkg/agent/interfaces"
	"github.com/xibodev/compa/v4/pkg/audio/tts"
	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/commands"
	"github.com/xibodev/compa/v4/pkg/config"
	runtimeevents "github.com/xibodev/compa/v4/pkg/events"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/providers"
	"github.com/xibodev/compa/v4/pkg/skills"
	"github.com/xibodev/compa/v4/pkg/state"
	"github.com/xibodev/compa/v4/pkg/tools"
)

// NewAgentLoop creates the agent loop over cfg. Each agent runs on its
// model selection, resolved through the loop's model resolver (see
// WithModelResolver). provider, when not nil, serves the default selection
// instead, as a single candidate and with no resolution — the seam embedders
// and tests inject a provider through. An agent whose selection is empty or
// does not resolve has no model: its turns without a per-message selection
// fail with a no-model error.
func NewAgentLoop(
	cfg *config.Config,
	msgBus *bus.MessageBus,
	provider providers.LLMProvider,
	opts ...AgentLoopOption,
) *AgentLoop {
	// Turns of different sessions run in parallel, up to max_parallel_turns;
	// a session's own turns always run one after another.
	workerPoolSize := cfg.Agents.Defaults.MaxParallelTurns
	if workerPoolSize <= 0 {
		workerPoolSize = defaultMaxParallelTurns
	}

	al := &AgentLoop{
		bus:               msgBus,
		cfg:               cfg,
		provider:          provider,
		cmdRegistry:       commands.NewRegistry(commands.BuiltinDefinitions()),
		steering:          newSteeringQueue(parseSteeringMode(cfg.Agents.Defaults.SteeringMode)),
		workerSem:         make(chan struct{}, workerPoolSize),
		ownsRuntimeEvents: true,
		approvals:         newOwnerApprovals(),
	}
	al.lifetime, al.endLifetime = context.WithCancel(context.Background())
	for _, opt := range opts {
		if opt != nil {
			opt(al)
		}
	}
	if al.resolveModel == nil {
		al.resolveModel = defaultModelResolver()
	}

	registry := NewAgentRegistry(cfg, provider, al.resolveModel)
	al.registry = registry

	// Set up the shared failover: core's candidate executor over one
	// instance health tracker, with every instance's RPM (its runtime
	// setting) registered with its rate limiter so the limit holds before
	// each LLM call.
	al.rateLimits = newCandidateRateLimits()
	al.rateLimits.register(registry.candidates())
	al.failover = providers.NewFailover(al.rateLimits.registry)

	// Create state manager using default agent's workspace for channel recording
	if defaultAgent := registry.GetDefaultAgent(); defaultAgent != nil {
		al.state = state.NewManager(defaultAgent.Workspace)
	}

	bridge, err := newEvolutionBridge(registry, cfg, registryModelProvider(registry))
	if err != nil {
		logger.WarnCF("agent", "Failed to initialize evolution bridge", map[string]any{
			"error": err.Error(),
		})
	}
	al.evolution = bridge

	if al.runtimeEvents == nil {
		al.runtimeEvents = runtimeevents.NewBus()
		al.ownsRuntimeEvents = true
	}
	if bridge != nil {
		bridge.setCurrentCheck(al.isCurrentEvolutionBridge)
		if err := bridge.subscribeRuntimeEvents(al.runtimeEvents.Channel()); err != nil {
			logger.WarnCF("agent", "Failed to subscribe evolution bridge to runtime events", map[string]any{
				"error": err.Error(),
			})
		}
	}
	al.activeReqCond = sync.NewCond(&al.activeReqMu)
	al.refreshRuntimeEventLogger(cfg)
	al.hooks = NewHookManager(al.runtimeEvents.Channel())
	configureHookManagerFromConfig(al.hooks, cfg)
	al.contextManager = al.resolveContextManager()

	// Register shared tools to all agents (now that al is created)
	registerSharedTools(al, cfg, msgBus, registry)

	return al
}

// toolLogFilter returns the filter the tool registries pass the arguments
// and failures they log through: while logging.redact_secrets is on, the
// secrets Compa stores become [FILTERED].
func toolLogFilter(cfg *config.Config) func(string) string {
	if cfg == nil || !cfg.Logging.RedactSecrets {
		return nil
	}
	return func(text string) string { return cfg.SensitiveDataReplacer().Replace(text) }
}

func registerSharedTools(
	al *AgentLoop,
	cfg *config.Config,
	msgBus interfaces.MessageBus,
	registry *AgentRegistry,
) {
	allowReadPaths := buildAllowReadPatterns(cfg)
	var ttsProvider tts.TTSProvider
	if cfg.Tools.IsToolEnabled("send_tts") {
		ttsProvider = tts.DetectTTS(cfg)
		if ttsProvider == nil {
			logger.WarnCF("voice-tts", "send_tts enabled but no TTS provider configured", nil)
		}
	}

	messageTargets := cfg.Tools.Message.EffectiveTargets()
	logFilter := toolLogFilter(cfg)

	for _, agentID := range registry.ListAgentIDs() {
		agent, ok := registry.GetAgent(agentID)
		if !ok {
			continue
		}
		agent.Tools.SetSensitiveDataFilter(logFilter)

		if cfg.Tools.IsToolEnabled("web") {
			searchTool, err := tools.NewWebSearchTool(tools.WebSearchToolOptionsFromConfig(cfg))
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web search tool", map[string]any{"error": err.Error()})
			} else if searchTool != nil {
				agent.Tools.Register(searchTool)
			}
		}
		if cfg.Tools.IsToolEnabled("web_fetch") {
			fetchTool, err := tools.NewWebFetchToolWithProxy(
				50000,
				cfg.Tools.Web.Proxy,
				cfg.Tools.Web.Format,
				cfg.Tools.Web.FetchLimitBytes,
				cfg.Tools.Web.PrivateHostWhitelist)
			if err != nil {
				logger.ErrorCF("agent", "Failed to create web fetch tool", map[string]any{"error": err.Error()})
			} else {
				agent.Tools.Register(fetchTool)
			}
		}

		// Capabilities contributed from OUTSIDE the kernel.
		//
		// This block used to call internal/moduletools directly, which made the
		// kernel depend on the module host -- a Layer 1 -> Layer 2 inversion
		// that also made the kernel impossible to embed, since Go forbids an
		// external module from importing an internal/ package.
		//
		// Now the composition root supplies providers. Full Compa passes one
		// backed by the detached-module host and behaves exactly as before; a
		// standalone product passes a native in-process one and pays no
		// subprocess or protocol cost to host itself; a bare kernel passes none.
		//
		// Their tools register as extensions: a module tool never takes the
		// place of a built-in one of the same name.
		for _, provider := range al.toolProviders {
			summaries, knowledge := provider.RegisterTools(agent.Workspace, agent.Tools.RegisterExtension)
			agent.ModuleSummaries = append(agent.ModuleSummaries, summaries...)
			// The LAST non-nil loader wins rather than merging: a selection
			// resolves to one provider, and silently combining two loaders
			// would make an ambiguous selection look decided.
			if knowledge != nil {
				agent.ModuleKnowledge = knowledge
			}
		}

		// Hardware tools (I2C, SPI) - Linux only, returns error on other platforms
		if cfg.Tools.IsToolEnabled("i2c") {
			agent.Tools.Register(tools.NewI2CTool())
		}
		if cfg.Tools.IsToolEnabled("spi") {
			agent.Tools.Register(tools.NewSPITool())
		}
		if cfg.Tools.IsToolEnabled("serial") {
			agent.Tools.Register(tools.NewSerialTool())
		}

		// Message tool
		if cfg.Tools.IsToolEnabled("message") {
			messageTool := tools.NewMessageTool()
			messageTool.SetTargets(messageTargets)
			if cfg.Tools.Message.MediaEnabled {
				messageTool.ConfigureLocalMedia(
					agent.Workspace,
					cfg.Agents.Defaults.RestrictToWorkspace,
					cfg.Agents.Defaults.GetMaxMediaSize(),
					allowReadPaths,
				)
			}
			messageTool.SetSendCallback(func(
				ctx context.Context,
				channel, chatID, content, replyToMessageID string,
				mediaParts []bus.MediaPart,
			) error {
				outboundCtx := bus.NewOutboundContext(channel, chatID, replyToMessageID)
				outboundAgentID, outboundSessionKey, outboundScope := outboundTurnMetadata(
					tools.ToolAgentID(ctx),
					tools.ToolSessionKey(ctx),
					tools.ToolSessionScope(ctx),
				)
				if len(mediaParts) > 0 {
					outboundMedia := bus.OutboundMediaMessage{
						Channel:    channel,
						ChatID:     chatID,
						Context:    outboundCtx,
						AgentID:    outboundAgentID,
						SessionKey: outboundSessionKey,
						Scope:      outboundScope,
						Parts:      mediaParts,
					}
					if al.currentChannelManager() != nil && channel != "" {
						return al.currentChannelManager().SendMedia(ctx, outboundMedia)
					}
					pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer pubCancel()
					return msgBus.PublishOutboundMedia(pubCtx, outboundMedia)
				}
				outboundMessage := bus.OutboundMessage{
					Channel:          channel,
					ChatID:           chatID,
					Context:          outboundCtx,
					AgentID:          outboundAgentID,
					SessionKey:       outboundSessionKey,
					Scope:            outboundScope,
					Content:          content,
					ReplyToMessageID: replyToMessageID,
				}
				if al.currentChannelManager() != nil && channel != "" {
					return al.currentChannelManager().SendMessage(ctx, outboundMessage)
				}
				pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer pubCancel()
				return msgBus.PublishOutbound(pubCtx, outboundMessage)
			})
			agent.Tools.Register(messageTool)
		}
		if cfg.Tools.IsToolEnabled("reaction") {
			reactionTool := tools.NewReactionTool()
			reactionTool.SetTargets(messageTargets)
			reactionTool.SetReactionCallback(func(ctx context.Context, channel, chatID, messageID string) error {
				if al.currentChannelManager() == nil {
					return fmt.Errorf("channel manager not configured")
				}
				ch, ok := al.currentChannelManager().GetChannel(channel)
				if !ok {
					return fmt.Errorf("channel %s not found", channel)
				}
				rc, ok := ch.(channels.ReactionCapable)
				if !ok {
					return fmt.Errorf("channel %s does not support reactions", channel)
				}
				_, err := rc.ReactToMessage(ctx, chatID, messageID)
				return err
			})
			agent.Tools.Register(reactionTool)
		}

		// Send file tool (outbound media via MediaStore — store injected later by SetMediaStore)
		if cfg.Tools.IsToolEnabled("send_file") {
			sendFileTool := tools.NewSendFileTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			agent.Tools.Register(sendFileTool)
		}

		if ttsProvider != nil {
			agent.Tools.Register(tools.NewSendTTSTool(ttsProvider, nil))
		}

		if cfg.Tools.IsToolEnabled("load_image") {
			loadImageTool := tools.NewLoadImageTool(
				agent.Workspace,
				cfg.Agents.Defaults.RestrictToWorkspace,
				cfg.Agents.Defaults.GetMaxMediaSize(),
				nil,
				allowReadPaths,
			)
			agent.Tools.Register(loadImageTool)
		}

		// Skill discovery and installation tools
		skills_enabled := cfg.Tools.IsToolEnabled("skills")
		find_skills_enable := cfg.Tools.IsToolEnabled("find_skills")
		install_skills_enable := cfg.Tools.IsToolEnabled("install_skill")
		if skills_enabled && (find_skills_enable || install_skills_enable) {
			registryMgr := skills.NewRegistryManagerFromToolsConfig(cfg.Tools.Skills)

			if find_skills_enable {
				searchCache := skills.NewSearchCache(
					cfg.Tools.Skills.SearchCache.MaxSize,
					time.Duration(cfg.Tools.Skills.SearchCache.TTLSeconds)*time.Second,
				)
				agent.Tools.Register(tools.NewFindSkillsTool(registryMgr, searchCache))
			}

			if install_skills_enable {
				agent.Tools.Register(tools.NewInstallSkillTool(registryMgr, agent.Workspace))
			}
		}

		// Spawn and spawn_status tools share a SubagentManager.
		// Construct it when either tool is enabled (both require subagent).
		spawnEnabled := cfg.Tools.IsToolEnabled("spawn")
		spawnStatusEnabled := cfg.Tools.IsToolEnabled("spawn_status")
		if (spawnEnabled || spawnStatusEnabled) && cfg.Tools.IsToolEnabled("subagent") {
			subagentManager := tools.NewSubagentManager()
			if spawnEnabled {
				spawnTool := tools.NewSpawnTool(subagentManager)
				spawnTool.SetSpawner(NewSubTurnSpawner(al))
				currentAgentID := agentID
				spawnTool.SetAllowlistChecker(func(targetAgentID string) bool {
					return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
				})

				agent.Tools.Register(spawnTool)

				// Also register the synchronous subagent tool
				subagentTool := tools.NewSubagentTool(subagentManager)
				subagentTool.SetSpawner(NewSubTurnSpawner(al))
				agent.Tools.Register(subagentTool)
			}
			if spawnStatusEnabled {
				agent.Tools.Register(tools.NewSpawnStatusTool(subagentManager))
			}
		} else if (spawnEnabled || spawnStatusEnabled) && !cfg.Tools.IsToolEnabled("subagent") {
			logger.WarnCF("agent", "spawn/spawn_status tools require subagent to be enabled", nil)
		}

		// Register delegate tool for multi-agent setups.
		// Auto-enabled when multiple agents exist. Delegation uses the SubTurn
		// mechanism directly (not SubagentManager) and is independent of the
		// subagent tool.
		if len(registry.ListAgentIDs()) > 1 {
			delegateTool := tools.NewDelegateTool()
			delegateTool.SetSpawner(NewSubTurnSpawner(al))
			currentAgentID := agentID
			delegateTool.SetSelfAgentID(currentAgentID)
			delegateTool.SetAllowlistChecker(func(targetAgentID string) bool {
				return registry.CanSpawnSubagent(currentAgentID, targetAgentID)
			})
			agent.Tools.Register(delegateTool)
		}

		warnOnUnknownAgentToolDeclarations(agentID, agent.Workspace, agent.Definition, agent.Tools)
	}
}
