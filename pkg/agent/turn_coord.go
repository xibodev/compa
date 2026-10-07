// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/providers"
)

func (al *AgentLoop) runTurn(ctx context.Context, ts *turnState, pipeline *Pipeline) (_ turnResult, turnErr error) {
	turnCtx, turnCancel := context.WithCancel(ctx)
	defer turnCancel()
	ts.setTurnCancel(turnCancel)

	// Inject turnState and AgentLoop into context so tools (e.g. spawn) can retrieve them.
	turnCtx = withTurnState(turnCtx, ts)
	turnCtx = WithAgentLoop(turnCtx, al)

	al.registerActiveTurn(ts)
	defer al.clearActiveTurn(ts)

	if ts.origin == "" {
		ts.origin = turnOrigin(isScheduledTurn(ctx), ts.channel)
	}

	if al.takePendingStop(ts.sessionKey) {
		_ = ts.requestHardAbort()
	}

	turnStatus := TurnEndStatusCompleted
	defer func() {
		attemptedSkills := ts.attemptedSkillsSnapshot()
		skillContextSnapshots := ts.skillContextSnapshotsSnapshot()
		finalSuccessfulPath := []string(nil)
		if turnStatus == TurnEndStatusCompleted {
			if latest := ts.latestSkillContextSnapshot(); len(latest) > 0 {
				finalSuccessfulPath = latest
			} else {
				finalSuccessfulPath = append([]string(nil), attemptedSkills...)
			}
		}
		failure := ""
		if turnStatus == TurnEndStatusError && turnErr != nil {
			failure = turnErr.Error()
		}
		al.emitEvent(
			runtimeevents.KindAgentTurnEnd,
			ts.eventMeta("runTurn", "turn.end"),
			TurnEndPayload{
				Status:                turnStatus,
				Workspace:             ts.workspace,
				Iterations:            ts.currentIteration(),
				Duration:              time.Since(ts.startedAt),
				FinalContentLen:       ts.finalContentLen(),
				UserMessage:           ts.userMessage,
				FinalContent:          ts.finalContentSnapshot(),
				ActiveSkills:          append([]string(nil), ts.activeSkills...),
				AttemptedSkills:       attemptedSkills,
				FinalSuccessfulPath:   finalSuccessfulPath,
				SkillContextSnapshots: skillContextSnapshots,
				ToolKinds:             ts.toolKindsSnapshot(),
				ToolExecutions:        ts.toolExecutionsSnapshot(),
				Error:                 failure,
				FromOwner:             inboundFromOwner(ts.opts.Dispatch.InboundContext),
			},
		)
	}()

	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return al.abortTurn(ts)
	}

	al.emitEvent(
		runtimeevents.KindAgentTurnStart,
		ts.eventMeta("runTurn", "turn.start"),
		TurnStartPayload{
			UserMessage: ts.userMessage,
			MediaCount:  len(ts.media),
		},
	)

	// SetupTurn extracts the one-time initialization phase.
	exec, err := pipeline.SetupTurn(turnCtx, ts)
	if err != nil {
		turnStatus = TurnEndStatusError
		return turnResult{}, err
	}
	defer exec.closeOwnedProviders()
	defer func() {
		// /show model reports what last answered the agent's own model.
		if turnStatus == TurnEndStatusCompleted && exec.requestedSelection == "" {
			ts.agent.noteServed(exec.lastServed)
		}
	}()

	// Convenience references to exec fields used throughout the turn loop.
	messages := exec.messages
	pendingMessages := exec.pendingMessages
	maxMediaSize := pipeline.Cfg.Agents.Defaults.GetMaxMediaSize()
	finalContent := exec.finalContent

	for ts.currentIteration() < ts.agent.MaxIterations || len(pendingMessages) > 0 || len(exec.pendingMessages) > 0 {
		if ts.hardAbortRequested() {
			turnStatus = TurnEndStatusAborted
			return al.abortTurn(ts)
		}

		iteration := ts.currentIteration() + 1
		ts.setIteration(iteration)
		ts.setPhase(TurnPhaseRunning)

		if iteration > 1 {
			// For subsequent iterations, read from exec.pendingMessages which
			// is where ExecuteTools (or initial poll) deposits steering.
			// We do NOT call dequeueSteeringMessagesForScope here because
			// steering was already consumed from al.steering by ExecuteTools.
			if len(exec.pendingMessages) > 0 {
				pendingMessages = append(pendingMessages, exec.pendingMessages...)
				exec.pendingMessages = nil
			}
		} else if !ts.opts.SkipInitialSteeringPoll {
			if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
				pendingMessages = append(pendingMessages, steerMsgs...)
			}
		}

		// A non-critical sub-turn stops once its parent turn has ended.
		if ts.parentTurnState != nil && ts.IsParentEnded() {
			if !ts.critical {
				logger.InfoCF("agent", "Parent turn ended, non-critical SubTurn exiting gracefully", map[string]any{
					"agent_id":  ts.agentID,
					"iteration": iteration,
					"turn_id":   ts.turnID,
				})
				break
			}
			logger.InfoCF("agent", "Parent turn ended, critical SubTurn continues running", map[string]any{
				"agent_id":  ts.agentID,
				"iteration": iteration,
				"turn_id":   ts.turnID,
			})
		}

		// Poll for pending SubTurn results
		if msg, ok := pipeline.takeSubTurnResult(ts); ok {
			pendingMessages = append(pendingMessages, msg)
		}

		// Inject pending steering messages
		if len(pendingMessages) > 0 {
			resolvedPending := resolveMediaRefs(pendingMessages, pipeline.MediaStore, maxMediaSize, 0)
			totalContentLen := 0
			for i, pm := range pendingMessages {
				messages = append(messages, resolvedPending[i])
				totalContentLen += len(pm.Content)
				if !ts.opts.NoHistory {
					ts.agent.Sessions.AddFullMessage(ts.sessionKey, pm)
					ts.recordPersistedMessage(pm)
					ts.ingestMessage(turnCtx, al, pm)
				}
				logger.InfoCF("agent", "Injected steering message into context",
					map[string]any{
						"agent_id":    ts.agent.ID,
						"iteration":   iteration,
						"content_len": len(pm.Content),
						"media_count": len(pm.Media),
					})
			}
			al.emitEvent(
				runtimeevents.KindAgentSteeringInjected,
				ts.eventMeta("runTurn", "turn.steering.injected"),
				SteeringInjectedPayload{
					Count:           len(pendingMessages),
					TotalContentLen: totalContentLen,
				},
			)
			// Clear exec.pendingMessages after injection so InitialSteeringMessages
			// are not re-injected on subsequent iterations (Issue 2 fix).
			exec.pendingMessages = nil
		}
		// Always sync messages into exec.messages so CallLLM sees the updated state
		exec.messages = messages

		logger.DebugCF("agent", "LLM iteration",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"iteration": iteration,
				"max":       ts.agent.MaxIterations,
			})

		// Execute LLM call via Pipeline
		ts.setPhase(TurnPhaseRunning)
		ctrl, callErr := pipeline.CallLLM(ctx, turnCtx, ts, exec, iteration)
		if callErr != nil {
			turnStatus = TurnEndStatusError
			return turnResult{}, callErr
		}
		messages = exec.messages
		// Take the steering CallLLM queued: it is injected once, at the top
		// of the next iteration, from pendingMessages alone.
		pendingMessages = exec.pendingMessages
		exec.pendingMessages = nil
		finalContent = exec.finalContent

		switch ctrl {
		case ControlContinue:
			continue
		case ControlBreak:
			// Hard abort: delegate to abortTurn (sets TurnEndStatusAborted)
			if exec.abortedByHardAbort {
				turnStatus = TurnEndStatusAborted
				return al.abortTurn(ts)
			}
			// Hook abort (HookActionAbortTurn): sets TurnEndStatusError, returns error
			if exec.abortedByHook {
				turnStatus = TurnEndStatusError
				return turnResult{}, fmt.Errorf("hook requested turn abort")
			}
			// Ensure empty response falls back to DefaultResponse
			if finalContent == "" {
				finalContent = ts.opts.DefaultResponse
			}
			result, finalizeErr := pipeline.Finalize(ctx, turnCtx, ts, exec, turnStatus, finalContent)
			if finalizeErr != nil {
				turnStatus = TurnEndStatusError
			}
			return result, finalizeErr
		case ControlToolLoop:
			// Execute tools via Pipeline
			toolCtrl := pipeline.ExecuteTools(ctx, turnCtx, ts, exec, iteration)
			switch toolCtrl {
			case ToolControlContinue:
				// Re-read exec.messages since ExecuteTools may have updated it
				// (added tool results/skipped messages) before returning ControlContinue
				messages = exec.messages
				continue
			case ToolControlBreak:
				// Hard abort: delegate to abortTurn (sets TurnEndStatusAborted)
				if exec.abortedByHardAbort {
					turnStatus = TurnEndStatusAborted
					return al.abortTurn(ts)
				}
				// Hook abort (HookActionAbortTurn): sets TurnEndStatusError, returns error
				if exec.abortedByHook {
					turnStatus = TurnEndStatusError
					return turnResult{}, fmt.Errorf("hook requested turn abort")
				}
				// ExecuteTools returned ControlBreak:
				// - allResponsesHandled=true: finalize without DefaultResponse (exec.finalContent empty)
				// - allResponsesHandled=false: coordinator applies DefaultResponse before finalize
				if exec.allResponsesHandled {
					finalContent = ""
				}
				result, finalizeErr := pipeline.Finalize(ctx, turnCtx, ts, exec, turnStatus, finalContent)
				if finalizeErr != nil {
					turnStatus = TurnEndStatusError
				}
				return result, finalizeErr
			}
		}
	}

	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return al.abortTurn(ts)
	}

	if finalContent == "" {
		if ts.currentIteration() >= ts.agent.MaxIterations && ts.agent.MaxIterations > 0 {
			finalContent = toolLimitResponse
		} else {
			finalContent = ts.opts.DefaultResponse
		}
	}

	// Check hard abort before finalizing (may have been set during tool execution)
	if ts.hardAbortRequested() {
		turnStatus = TurnEndStatusAborted
		return al.abortTurn(ts)
	}

	result, err := pipeline.Finalize(ctx, turnCtx, ts, exec, turnStatus, finalContent)
	if err != nil {
		turnStatus = TurnEndStatusError
	}
	return result, err
}

func (al *AgentLoop) abortTurn(ts *turnState) (turnResult, error) {
	ts.setPhase(TurnPhaseAborted)
	if !ts.opts.NoHistory {
		if err := ts.restoreSession(ts.agent); err != nil {
			al.emitEvent(
				runtimeevents.KindAgentError,
				ts.eventMeta("abortTurn", "turn.error"),
				ErrorPayload{
					Stage:   "session_restore",
					Message: err.Error(),
				},
			)
			return turnResult{}, err
		}
	}
	return turnResult{status: TurnEndStatusAborted}, nil
}

func (al *AgentLoop) selectCandidates(
	agent *AgentInstance,
	userMsg string,
	history []providers.Message,
) (candidates []providers.FallbackCandidate, model string, usedLight bool) {
	if agent.Router == nil || len(agent.LightCandidates) == 0 {
		return agent.Candidates, primaryModelID(agent.Candidates, ""), false
	}

	_, usedLight, score := agent.Router.SelectModel(userMsg, history, agent.Model)
	if !usedLight {
		logger.DebugCF("agent", "Model routing: primary model selected",
			map[string]any{
				"agent_id":  agent.ID,
				"score":     score,
				"threshold": agent.Router.Threshold(),
			})
		return agent.Candidates, primaryModelID(agent.Candidates, ""), false
	}

	logger.InfoCF("agent", "Model routing: light model selected",
		map[string]any{
			"agent_id":    agent.ID,
			"light_model": agent.Router.LightModel(),
			"score":       score,
			"threshold":   agent.Router.Threshold(),
		})
	return agent.LightCandidates, primaryModelID(agent.LightCandidates, ""), true
}

func (al *AgentLoop) resolveContextManager() ContextManager {
	name := al.GetConfig().Agents.Defaults.ContextManager
	if name == "" || name == defaultContextManagerName {
		return &defaultContextManager{al: al}
	}
	factory, ok := lookupContextManager(name)
	if !ok {
		logger.WarnCF("agent", "Unknown context manager, falling back to default", map[string]any{
			"name": name,
		})
		return &defaultContextManager{al: al}
	}
	cm, err := factory(al.GetConfig().Agents.Defaults.ContextManagerConfig, al)
	if err != nil {
		logger.WarnCF("agent", "Failed to create context manager, falling back to default", map[string]any{
			"name":  name,
			"error": err.Error(),
		})
		return &defaultContextManager{al: al}
	}
	return cm
}

// contextManagerToolRegistrar is a context manager that contributes tools to
// the agents.
type contextManagerToolRegistrar interface {
	registerTools(al *AgentLoop)
}

// reloadContextManager gives a reload's new agents a context manager: the
// current one when previous and cfg configure it alike, with its tools
// registered on the new agents, and otherwise one built from cfg. It returns
// the manager it replaced, for the caller to close once turns drain, or nil.
func (al *AgentLoop) reloadContextManager(previous, cfg *config.Config) ContextManager {
	current := al.currentContextManager()
	if current != nil && previous != nil && cfg != nil &&
		previous.Agents.Defaults.ContextManager == cfg.Agents.Defaults.ContextManager &&
		bytes.Equal(previous.Agents.Defaults.ContextManagerConfig, cfg.Agents.Defaults.ContextManagerConfig) {
		if registrar, ok := current.(contextManagerToolRegistrar); ok {
			registrar.registerTools(al)
		}
		return nil
	}
	next := al.resolveContextManager()
	al.mu.Lock()
	al.contextManager = next
	al.mu.Unlock()
	return current
}

// closeContextManager closes cm when it holds resources.
func closeContextManager(cm ContextManager) {
	closer, ok := cm.(interface{ Close() error })
	if !ok || closer == nil {
		return
	}
	if err := closer.Close(); err != nil {
		logger.ErrorCF("agent", "Failed to close context manager", map[string]any{"error": err.Error()})
	}
}

func (al *AgentLoop) askSideQuestion(
	ctx context.Context,
	agent *AgentInstance,
	opts *processOptions,
	question string,
) (string, error) {
	if agent == nil {
		return "", fmt.Errorf("askSideQuestion: no agent available for /btw")
	}
	agent, releaseModel, _ := agent.turnSnapshot()
	defer releaseModel()

	question = strings.TrimSpace(question)
	if question == "" {
		return "", fmt.Errorf("askSideQuestion: %w", fmt.Errorf("Usage: /btw <question>"))
	}

	if opts != nil {
		resolved, err := resolveTurnProfileOptions(al.GetConfig(), *opts)
		if err != nil {
			return "", err
		}
		*opts = resolved
	}

	var media []string
	var channel, chatID, senderID, senderDisplayName string
	if opts != nil {
		media = opts.Dispatch.Media
		channel = opts.Dispatch.Channel()
		chatID = opts.Dispatch.ChatID()
		senderID = opts.Dispatch.SenderID()
		senderDisplayName = opts.SenderDisplayName
	}

	// Build messages with context but WITHOUT adding to session history
	var history []providers.Message
	var summary string
	if opts != nil && !opts.NoHistory {
		if resp, err := al.currentContextManager().Assemble(ctx, &AssembleRequest{
			SessionKey: opts.Dispatch.SessionKey,
			Budget:     agent.ContextWindow,
			MaxTokens:  agent.MaxTokens,
			AgentID:    agent.ID,
		}); err == nil && resp != nil {
			history = resp.History
			summary = resp.Summary
		}
	}

	var promptReq PromptBuildRequest
	if opts == nil {
		promptReq = PromptBuildRequest{
			History:           history,
			Summary:           summary,
			CurrentMessage:    question,
			Media:             append([]string(nil), media...),
			Channel:           channel,
			ChatID:            chatID,
			SenderID:          senderID,
			SenderDisplayName: senderDisplayName,
		}
	} else {
		promptReq = promptBuildRequestForProcessOptions(
			agent,
			*opts,
			history,
			summary,
			question,
			media,
		)
	}
	promptReq.SuppressToolUseRule = true
	promptReq.ToolUseFallback = false
	messages := agent.ContextBuilder.BuildMessagesFromPrompt(promptReq)

	maxMediaSize := al.GetConfig().Agents.Defaults.GetMaxMediaSize()
	currentTurnStart := len(messages)
	if strings.TrimSpace(question) != "" || len(media) > 0 {
		currentTurnStart = len(messages) - 1
	}
	messages = resolveMediaRefs(messages, al.currentMediaStore(), maxMediaSize, currentTurnStart)

	// The question runs on the message's own selection when it has one, as a
	// turn would, and on the agent's model otherwise.
	var selection string
	if opts != nil {
		selection = opts.Dispatch.ModelSelection()
	}
	models := &sideQuestionModels{agent: agent}
	if selection != "" {
		resolution, err := al.resolveTurnSelection(selection)
		if err != nil {
			return "", fmt.Errorf("model selection %q is not available: %w", selection, err)
		}
		models = newSideQuestionResolution(resolution)
	} else {
		if !agent.hasModel() {
			return "", agent.noModelError()
		}
		models.candidates, _, _ = al.selectCandidates(agent, question, messages)
	}
	defer func() { models.close() }()

	llmOpts := map[string]any{
		"max_tokens":       agent.MaxTokens,
		"temperature":      agent.Temperature,
		"prompt_cache_key": agent.ID + ":btw",
	}

	sideSuppressReasoning := false
	callCandidate := func(
		ctx context.Context,
		candidate providers.FallbackCandidate,
		callMessages []providers.Message,
	) (*providers.LLMResponse, error) {
		provider, err := models.provider(candidate)
		if err != nil {
			return nil, err
		}
		callOpts := llmOpts
		settings := thinkingSettingsFromCallSpec(models.config(candidate))
		sideSuppressReasoning = shouldSuppressReasoningFor(settings)
		if _, exists := callOpts["thinking_level"]; !exists && settings.configured {
			callOpts = shallowCloneLLMOptions(llmOpts)
			applyThinkingOption(callOpts, provider, settings, false, agent.ID)
		}
		return provider.Chat(ctx, callMessages, nil, candidate.Model, callOpts)
	}

	turnCtx := newTurnContext(nil, nil, nil)
	if opts != nil {
		turnCtx = newTurnContext(opts.Dispatch.InboundContext, opts.Dispatch.RouteResult, opts.Dispatch.SessionScope)
	}
	llmModel := primaryModelID(models.candidates, "")
	if al.hooks != nil {
		llmReq, decision := al.hooks.BeforeLLM(ctx, &LLMHookRequest{
			Meta: HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.request",
				turnContext: cloneTurnContext(turnCtx),
			},
			Context:  cloneTurnContext(turnCtx),
			Model:    llmModel,
			Messages: messages,
			Tools:    nil,
			Options:  llmOpts,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmReq != nil {
				if rewritten := strings.TrimSpace(llmReq.Model); rewritten != "" && rewritten != llmModel {
					// A hook-selected model runs on its own targets, never on
					// the pre-hook candidates.
					if selection != "" {
						return "", hookRewriteRejectedError(rewritten, selection)
					}
					resolution, err := al.resolveTurnSelection(rewritten)
					if err != nil {
						return "", fmt.Errorf("hook-selected model %q is not available: %w", rewritten, err)
					}
					models.close()
					models = newSideQuestionResolution(resolution)
					llmModel = primaryModelID(models.candidates, rewritten)
				}
				messages = llmReq.Messages
				llmOpts = llmReq.Options
				delete(llmOpts, "native_search")
			}
		case HookActionAbortTurn:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during before_llm: %s", reason)
		case HookActionHardAbort:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during before_llm: %s", reason)
		}
	}

	callSideLLM := func(callMessages []providers.Message) (*providers.LLMResponse, error) {
		result, err := al.currentFailover().Execute(
			ctx,
			models.candidates,
			func(ctx context.Context, candidate providers.FallbackCandidate) (*providers.LLMResponse, error) {
				return callCandidate(ctx, candidate, callMessages)
			},
		)
		if err != nil {
			return nil, err
		}
		return result.Value, nil
	}

	// Retry without media if vision is unsupported
	// Note: Vision retry is only applied to the initial call. If fallback chain
	// is used, vision errors from fallback providers will not trigger retry.
	var resp *providers.LLMResponse
	var err error
	resp, err = callSideLLM(messages)
	if err != nil && hasMediaRefs(messages) && isVisionUnsupportedError(err) {
		al.emitEvent(
			runtimeevents.KindAgentLLMRetry,
			HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.retry",
				turnContext: cloneTurnContext(turnCtx),
			},
			LLMRetryPayload{
				Attempt:    1,
				MaxRetries: 1,
				Reason:     "vision_unsupported",
				Error:      err.Error(),
				Backoff:    0,
			},
		)
		messagesWithoutMedia := stripMessageMedia(messages)
		resp, err = callSideLLM(messagesWithoutMedia)
	}
	if err != nil {
		return "", err
	}
	if resp == nil {
		return "", nil
	}

	// Apply after_llm hooks
	if al.hooks != nil {
		llmResp, decision := al.hooks.AfterLLM(ctx, &LLMHookResponse{
			Meta: HookMeta{
				Source:      "askSideQuestion",
				TracePath:   "turn.llm.response",
				turnContext: cloneTurnContext(turnCtx),
			},
			Context:  cloneTurnContext(turnCtx),
			Model:    llmModel,
			Response: resp,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmResp != nil && llmResp.Response != nil {
				resp = llmResp.Response
			}
		case HookActionAbortTurn, HookActionHardAbort:
			reason := decision.Reason
			if reason == "" {
				reason = "hook requested turn abort"
			}
			return "", fmt.Errorf("hook aborted turn during after_llm: %s", reason)
		}
	}
	if sideSuppressReasoning {
		resp.Reasoning = ""
		resp.ReasoningContent = ""
		resp.ReasoningDetails = nil
	}

	return sideQuestionResponseContent(resp), nil
}

// sideQuestionModels provides a /btw question's candidates with their
// providers and request specs: the agent's own, or those of a resolution the
// question owns and closes when it is answered.
type sideQuestionModels struct {
	candidates []providers.FallbackCandidate
	agent      *AgentInstance
	resolution *providers.InstanceResolution
	owned      []providers.LLMProvider
}

func newSideQuestionResolution(resolution *providers.InstanceResolution) *sideQuestionModels {
	return &sideQuestionModels{
		candidates: append([]providers.FallbackCandidate(nil), resolution.Candidates...),
		resolution: resolution,
	}
}

func (m *sideQuestionModels) provider(candidate providers.FallbackCandidate) (providers.LLMProvider, error) {
	if m.resolution == nil {
		return m.agent.providerForCandidate(candidate)
	}
	provider, err := m.resolution.ProviderForCandidate(candidate)
	if err != nil {
		return nil, err
	}
	m.owned = append(m.owned, provider)
	return provider, nil
}

func (m *sideQuestionModels) config(candidate providers.FallbackCandidate) *providers.CallSpec {
	if m.resolution == nil {
		return m.agent.configForCandidate(candidate)
	}
	modelCfg, err := m.resolution.CallSpecForCandidate(candidate)
	if err != nil {
		return nil
	}
	return modelCfg
}

func (m *sideQuestionModels) close() {
	closeUniqueStatefulProviders(m.owned...)
	m.owned = nil
}
