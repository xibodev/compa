// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v3/pkg/constants"
	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/session"
)

// CallLLM performs an LLM call with fallback support, hook invocation, and retry logic.
// It handles PreLLM setup, the actual LLM invocation with retry, and AfterLLM processing.
// Returns Control indicating what the coordinator should do next.
func (p *Pipeline) CallLLM(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	iteration int,
) (Control, error) {
	al := p.al
	maxMediaSize := p.Cfg.Agents.Defaults.GetMaxMediaSize()

	// PreLLM: resolve media refs (except on iteration 1 where user media is already resolved)
	if iteration > 1 {
		exec.messages = resolveMediaRefs(exec.messages, p.MediaStore, maxMediaSize, exec.currentTurnStart)
	}

	exec.providerToolDefs = al.offeredToolDefs(ts)

	// Native web search support. It searches inside the provider, where no
	// call can be asked about or refused: only a web_search the approval
	// policy allows is replaced by it.
	webSearchEnabled := p.Cfg.Tools.IsToolEnabled("web") && turnProfileToolAllowed(ts.profile, "web_search") &&
		al.toolAllowed(ts, "web_search")
	exec.useNativeSearch = webSearchEnabled && p.Cfg.Tools.Web.PreferNative &&
		func() bool {
			if ns, ok := exec.activeProvider.(providers.NativeSearchCapable); ok {
				return ns.SupportsNativeSearch()
			}
			return false
		}()
	if exec.useNativeSearch {
		filtered := make([]providers.ToolDefinition, 0, len(exec.providerToolDefs))
		for _, td := range exec.providerToolDefs {
			if td.Function.Name != "web_search" {
				filtered = append(filtered, td)
			}
		}
		exec.providerToolDefs = filtered
	}

	exec.callMessages = exec.messages
	if err := p.routeMediaTurn(ts, exec); err != nil {
		return ControlBreak, err
	}

	exec.llmOpts = map[string]any{
		"max_tokens":       ts.agent.MaxTokens,
		"temperature":      ts.agent.Temperature,
		"prompt_cache_key": ts.agent.ID,
	}
	if exec.useNativeSearch {
		exec.llmOpts["native_search"] = true
	}
	applyTurnThinkingOptions(exec, ts.agent, exec.activeProvider, true)

	exec.llmModel = exec.activeModel
	nativeSearchBeforeHook := exec.useNativeSearch

	// BeforeLLM hook
	if p.Hooks != nil {
		llmReq, decision := p.Hooks.BeforeLLM(turnCtx, &LLMHookRequest{
			Meta:     ts.eventMeta("runTurn", "turn.llm.request"),
			Context:  cloneTurnContext(ts.turnCtx),
			Model:    exec.llmModel,
			Messages: exec.callMessages,
			Tools:    exec.providerToolDefs,
			Options:  exec.llmOpts,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmReq != nil {
				prevModel := exec.llmModel
				exec.llmModel = llmReq.Model
				exec.callMessages = llmReq.Messages
				exec.providerToolDefs = filterToolsByTurnProfile(llmReq.Tools, ts.profile)
				exec.llmOpts = llmReq.Options
				if strings.TrimSpace(exec.llmModel) != "" && exec.llmModel != prevModel {
					if err := p.applyBeforeLLMModelRewrite(ts, exec); err != nil {
						return ControlBreak, err
					}
					applyTurnThinkingOptions(exec, ts.agent, exec.activeProvider, true)
				}
			}
		case HookActionAbortTurn:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			exec.abortedByHook = true
			return ControlBreak, nil
		case HookActionHardAbort:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
	}
	exec.useNativeSearch = webSearchEnabled && p.Cfg.Tools.Web.PreferNative &&
		func() bool {
			if ns, ok := exec.activeProvider.(providers.NativeSearchCapable); ok {
				return ns.SupportsNativeSearch()
			}
			return false
		}()
	if nativeSearchBeforeHook && !exec.useNativeSearch {
		exec.providerToolDefs = restoreToolDefinition(
			exec.providerToolDefs,
			al.offeredToolDefs(ts),
			"web_search",
		)
	}
	if exec.useNativeSearch {
		exec.providerToolDefs = filterClientWebSearch(exec.providerToolDefs)
		if exec.llmOpts == nil {
			exec.llmOpts = make(map[string]any)
		}
		exec.llmOpts["native_search"] = true
	} else {
		delete(exec.llmOpts, "native_search")
	}
	if exec.activeProvider == nil {
		model := strings.TrimSpace(exec.llmModelName)
		if model == "" {
			model = strings.TrimSpace(exec.llmModel)
		}
		if model == "" {
			return ControlBreak, fmt.Errorf("no active LLM provider configured; select and configure a model before starting a turn")
		}
		return ControlBreak, fmt.Errorf("no active LLM provider configured for model %q", model)
	}

	al.emitEvent(
		runtimeevents.KindAgentLLMRequest,
		ts.eventMeta("runTurn", "turn.llm.request"),
		LLMRequestPayload{
			Model:         exec.llmModel,
			MessagesCount: len(exec.callMessages),
			ToolsCount:    len(exec.providerToolDefs),
			MaxTokens:     ts.agent.MaxTokens,
			Temperature:   ts.agent.Temperature,
		},
	)

	logger.DebugCF("agent", "LLM request",
		map[string]any{
			"agent_id":          ts.agent.ID,
			"iteration":         iteration,
			"model":             exec.llmModel,
			"messages_count":    len(exec.callMessages),
			"tools_count":       len(exec.providerToolDefs),
			"max_tokens":        ts.agent.MaxTokens,
			"temperature":       ts.agent.Temperature,
			"system_prompt_len": len(exec.callMessages[0].Content),
		})
	logger.DebugCF("agent", "Full LLM request",
		map[string]any{
			"iteration":     iteration,
			"messages_json": formatMessagesForLog(exec.callMessages),
			"tools_json":    formatToolsForLog(exec.providerToolDefs),
		})

	// LLM call closure with fallback support
	callLLM := func(
		messagesForCall []providers.Message,
		toolDefsForCall []providers.ToolDefinition,
	) (*providers.LLMResponse, error) {
		providerCtx, providerCancel := context.WithCancel(turnCtx)
		ts.setProviderCancel(providerCancel)
		defer func() {
			providerCancel()
			ts.clearProviderCancel(providerCancel)
		}()

		al.activeRequestsInc()
		defer al.activeRequestsDec()

		if response, handled, streamErr := p.tryCandidateStreamingLLM(
			providerCtx,
			ts,
			exec,
			messagesForCall,
			toolDefsForCall,
		); handled {
			return response, streamErr
		}

		if len(exec.activeCandidates) == 0 {
			// An embedder-injected provider without candidates.
			return exec.activeProvider.Chat(providerCtx, messagesForCall, toolDefsForCall, exec.llmModel, exec.llmOpts)
		}

		runCandidate := func(
			ctx context.Context,
			candidate providers.FallbackCandidate,
		) (*providers.LLMResponse, error) {
			candidateProvider, err := exec.providerFor(candidate)
			if err != nil {
				return nil, err
			}
			callOpts := shallowCloneLLMOptions(exec.llmOpts)
			delete(callOpts, "thinking_level")
			candidateTools := toolDefsForCall
			candidateNativeSearch := webSearchEnabled && p.Cfg.Tools.Web.PreferNative &&
				func() bool {
					if ns, ok := candidateProvider.(providers.NativeSearchCapable); ok {
						return ns.SupportsNativeSearch()
					}
					return false
				}()
			if candidateNativeSearch {
				candidateTools = filterClientWebSearch(candidateTools)
				callOpts["native_search"] = true
			} else {
				delete(callOpts, "native_search")
				if exec.useNativeSearch {
					candidateTools = restoreToolDefinition(
						candidateTools,
						al.offeredToolDefs(ts),
						"web_search",
					)
				}
			}
			candidateThinking := thinkingSettingsFromCallSpec(exec.configFor(candidate))
			applyThinkingOption(callOpts, candidateProvider, candidateThinking, true, ts.agent.ID)
			exec.suppressReasoning = shouldSuppressReasoningFor(candidateThinking)
			return candidateProvider.Chat(ctx, messagesForCall, candidateTools, candidate.Model, callOpts)
		}

		// Every call runs through core's candidate executor, a single
		// candidate included, so each instance's health and RPM limit hold.
		if len(exec.activeCandidates) == 1 {
			result, err := p.Failover.Execute(providerCtx, exec.activeCandidates, func(
				ctx context.Context,
				_ providers.FallbackCandidate,
			) (*providers.LLMResponse, error) {
				if response, handled, streamErr := p.tryConfiguredStreamingLLM(
					ctx,
					ts,
					exec,
					messagesForCall,
					toolDefsForCall,
				); handled {
					return response, streamErr
				}
				return exec.activeProvider.Chat(ctx, messagesForCall, toolDefsForCall, exec.llmModel, exec.llmOpts)
			})
			if err != nil {
				return nil, err
			}
			exec.noteServed(exec.activeCandidates[0])
			return result.Value, nil
		}

		result, err := p.Failover.Execute(providerCtx, exec.activeCandidates, runCandidate)
		if err != nil {
			return nil, err
		}
		exec.serveResult(result)
		return result.Value, nil
	}
	// Retry loop
	var err error
	maxRetries := p.Cfg.Agents.Defaults.MaxLLMRetries
	if maxRetries <= 0 {
		maxRetries = 2
	}
	backoffSecs := p.Cfg.Agents.Defaults.LLMRetryBackoffSecs
	if backoffSecs <= 0 {
		backoffSecs = 2
	}
	// beforeCompaction holds the session as it was before a context retry
	// compacted it; when the retry fails anyway the history comes back, so a
	// failed call does not cost the conversation half its history.
	var beforeCompaction *sessionSnapshot
	for retry := 0; retry <= maxRetries; retry++ {
		exec.response, err = callLLM(exec.callMessages, exec.providerToolDefs)
		if err == nil {
			break
		}
		if ts.hardAbortRequested() && errors.Is(err, context.Canceled) {
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
		if isAfterOutputError(err) {
			break
		}

		if hasMediaRefs(exec.callMessages) && isVisionUnsupportedError(err) {
			return ControlBreak, visionUnsupportedModelError(
				exec.llmModelName,
				exec.requestedSelection != "",
				len(ts.agent.ImageCandidates) > 0,
			)
		}

		if len(exec.providerToolDefs) > 0 && isToolUnsupportedError(strings.ToLower(errorChainText(err))) {
			if ts.agent.Definition.requiresTools() {
				logger.WarnCF("agent", "Model does not support tools and the agent requires them; failing the turn", map[string]any{
					"model": exec.llmModelName,
					"error": err.Error(),
				})
				al.emitEvent(
					runtimeevents.KindAgentError,
					ts.eventMeta("runTurn", "turn.error"),
					ErrorPayload{
						Stage:   "llm",
						Message: ErrToolsRequired.Error(),
					},
				)
				return ControlBreak, ErrToolsRequired
			}
			logger.WarnCF("agent", "Model does not support tools, retrying without tools in conversational mode", map[string]any{
				"model": exec.llmModelName,
				"error": err.Error(),
			})
			exec.providerToolDefs = nil
			exec.response, err = callLLM(exec.callMessages, nil)
			if err == nil {
				break
			}
		}

		backoff, isTransientError := providers.RetryDelay(
			err,
			time.Duration(retry+1)*time.Duration(backoffSecs)*time.Second,
			maxLLMRetryAfterWait,
		)
		retryReason := llmRetryReason(err)
		isContextError := !isTransientError && isContextOverflowError(err)

		if isTransientError && retry < maxRetries {
			al.emitEvent(
				runtimeevents.KindAgentLLMRetry,
				ts.eventMeta("runTurn", "turn.llm.retry"),
				LLMRetryPayload{
					Attempt:    retry + 1,
					MaxRetries: maxRetries,
					Reason:     retryReason,
					Error:      err.Error(),
					Backoff:    backoff,
				},
			)
			logger.WarnCF("agent", "Transient LLM error, retrying after backoff", map[string]any{
				"error":   err.Error(),
				"reason":  retryReason,
				"retry":   retry,
				"backoff": backoff.String(),
			})
			if sleepErr := sleepWithContext(turnCtx, backoff); sleepErr != nil {
				if ts.hardAbortRequested() {
					_ = ts.requestHardAbort()
					return ControlBreak, nil
				}
				err = sleepErr
				break
			}
			continue
		}

		if isContextError && retry < maxRetries && !ts.opts.NoHistory {
			al.emitEvent(
				runtimeevents.KindAgentLLMRetry,
				ts.eventMeta("runTurn", "turn.llm.retry"),
				LLMRetryPayload{
					Attempt:    retry + 1,
					MaxRetries: maxRetries,
					Reason:     "context_limit",
					Error:      err.Error(),
				},
			)
			logger.WarnCF(
				"agent",
				"Context window error detected, attempting compression",
				map[string]any{
					"error": err.Error(),
					"retry": retry,
				},
			)

			if retry == 0 && !constants.IsInternalChannel(ts.channel) {
				al.bus.PublishOutbound(ctx, outboundMessageForTurn(
					ts,
					"Context window exceeded. Compressing history and retrying...",
				))
			}

			if beforeCompaction == nil {
				beforeCompaction = takeSessionSnapshot(ts.agent.Sessions, ts.sessionKey)
			}
			if compactErr := p.ContextManager.Compact(ctx, &CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonRetry,
				Budget:     ts.agent.ContextWindow,
				AgentID:    ts.agent.ID,
			}); compactErr != nil {
				logger.WarnCF("agent", "Context overflow compact failed", map[string]any{
					"session_key": ts.sessionKey,
					"error":       compactErr.Error(),
				})
			}
			ts.refreshRestorePointFromSession(ts.agent)
			if asmResp, asmErr := p.ContextManager.Assemble(ctx, &AssembleRequest{
				SessionKey: ts.sessionKey,
				Budget:     ts.agent.ContextWindow,
				MaxTokens:  ts.agent.MaxTokens,
				AgentID:    ts.agent.ID,
			}); asmErr == nil && asmResp != nil {
				exec.history = asmResp.History
				exec.summary = asmResp.Summary
			}
			contextualSkills := ts.activeSkills
			if ts.agent.ContextBuilder != nil {
				contextualSkills = ts.agent.ContextBuilder.ResolveActiveSkillsForContext(ts.activeSkills)
			}
			ts.recordSkillContextSnapshot(skillContextTriggerContextRetryRebuild, contextualSkills)
			stableHistory, protectedTurnTail := splitHistoryForActiveTurn(
				exec.history,
				ts.persistedMessagesSnapshot(),
			)
			buildMessages := func(trimmedHistory []providers.Message) []providers.Message {
				fullHistory := append(append([]providers.Message(nil), trimmedHistory...), protectedTurnTail...)
				rebuildPromptReq := promptBuildRequestForTurn(ts, fullHistory, exec.summary, "", nil, p.Cfg)
				rebuildPromptReq.ActiveSkills = append([]string(nil), contextualSkills...)
				rebuilt := ts.agent.ContextBuilder.BuildMessagesFromPrompt(rebuildPromptReq)
				return resolveMediaRefs(
					rebuilt,
					p.MediaStore,
					maxMediaSize,
					len(rebuilt)-len(protectedTurnTail),
				)
			}
			originalHistoryCount := len(exec.history)
			var fit bool
			var trimmedStableHistory []providers.Message
			trimmedStableHistory, exec.callMessages, fit = trimHistoryToFitContextWindow(
				stableHistory,
				buildMessages,
				ts.agent.ContextWindow,
				exec.providerToolDefs,
				ts.agent.MaxTokens,
			)
			exec.history = append(trimmedStableHistory, protectedTurnTail...)
			exec.messages = buildMessages(trimmedStableHistory)
			exec.currentTurnStart = len(exec.messages) - len(protectedTurnTail)
			if dropped := originalHistoryCount - len(exec.history); dropped > 0 {
				logger.WarnCF("agent", "Trimmed rebuilt history after context retry compaction", map[string]any{
					"session_key":     ts.sessionKey,
					"retry":           retry,
					"dropped_msgs":    dropped,
					"remaining_msgs":  len(exec.history),
					"context_window":  ts.agent.ContextWindow,
					"max_tokens":      ts.agent.MaxTokens,
					"still_overlimit": !fit,
				})
			} else if !fit {
				logger.WarnCF("agent", "Context still exceeds budget after retry compaction rebuild", map[string]any{
					"session_key":         ts.sessionKey,
					"retry":               retry,
					"history_msgs":        len(exec.history),
					"protected_turn_msgs": len(protectedTurnTail),
					"context_window":      ts.agent.ContextWindow,
					"max_tokens":          ts.agent.MaxTokens,
				})
			}
			if !fit {
				err = fmt.Errorf(
					"context window still exceeded after retry compaction; refusing to drop active turn messages: %w",
					err,
				)
				break
			}
			continue
		}
		break
	}

	if err != nil {
		if beforeCompaction != nil && !ts.opts.NoHistory {
			beforeCompaction.restore(ts.agent.Sessions)
			ts.refreshRestorePointFromSession(ts.agent)
		}
		al.emitEvent(
			runtimeevents.KindAgentError,
			ts.eventMeta("runTurn", "turn.error"),
			ErrorPayload{
				Stage:   "llm",
				Message: err.Error(),
			},
		)
		logger.ErrorCF("agent", "LLM call failed",
			map[string]any{
				"agent_id":  ts.agent.ID,
				"iteration": iteration,
				"model":     exec.llmModel,
				"error":     err.Error(),
			})
		return ControlBreak, fmt.Errorf("LLM call failed after retries: %w", err)
	}

	// AfterLLM hook
	if p.Hooks != nil {
		llmResp, decision := p.Hooks.AfterLLM(turnCtx, &LLMHookResponse{
			Meta:     ts.eventMeta("runTurn", "turn.llm.response"),
			Context:  cloneTurnContext(ts.turnCtx),
			Model:    exec.llmModel,
			Response: exec.response,
		})
		switch decision.normalizedAction() {
		case HookActionContinue, HookActionModify:
			if llmResp != nil && llmResp.Response != nil {
				exec.response = llmResp.Response
			}
		case HookActionAbortTurn:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			exec.abortedByHook = true
			return ControlBreak, nil
		case HookActionHardAbort:
			cancelConfiguredStreamingLLM(turnCtx, exec)
			_ = ts.requestHardAbort()
			exec.abortedByHardAbort = true
			return ControlBreak, nil
		}
	}

	// Save the usage on the turn state, which the streaming publisher reads
	// via GetLastUsage at finalize. Use ts directly: the raw ctx passed to
	// CallLLM is not seeded with turnState (only turnCtx is).
	if ts != nil && exec.response.Usage != nil {
		ts.SetLastUsage(exec.response.Usage)
	}

	if exec.suppressReasoning {
		exec.response.Reasoning = ""
		exec.response.ReasoningContent = ""
		exec.response.ReasoningDetails = nil
	}
	reasoningContent := responseReasoningContent(exec.response)
	shouldPublishWebToolCallInterim := ts.channel == "web" && len(exec.response.ToolCalls) > 0
	if shouldPublishWebToolCallInterim {
		// Web chat tool-call turns publish their reasoning/content/tool summary as a
		// structured sequence after the tool-call payload is normalized below.
	} else if ts.channel == "web" {
		if exec.streamingPublisher != nil && exec.streamingPublisher.ReasoningPublished() {
			if err := exec.streamingPublisher.FinalizeReasoning(turnCtx, reasoningContent); err != nil {
				logger.WarnCF("agent", "Failed to finalize streamed web reasoning", map[string]any{
					"channel": ts.channel,
					"chat_id": ts.chatID,
					"error":   err.Error(),
				})
			}
		} else {
			// Publish web thoughts before the turn context is canceled at return time.
			// The async variant can race with turn teardown and intermittently drop the
			// thought message in CI even though the LLM produced reasoning content.
			al.publishWebReasoning(turnCtx, reasoningContent, ts.chatID, ts.sessionKey, exec.llmModelName)
		}
	} else {
		go al.handleReasoning(
			turnCtx,
			reasoningContent,
			ts.channel,
			al.targetReasoningChannelID(ts.channel),
		)
	}
	al.emitEvent(
		runtimeevents.KindAgentLLMResponse,
		ts.eventMeta("runTurn", "turn.llm.response"),
		LLMResponsePayload{
			ContentLen:   len(exec.response.Content),
			ToolCalls:    len(exec.response.ToolCalls),
			HasReasoning: exec.response.Reasoning != "" || exec.response.ReasoningContent != "",
		},
	)

	llmResponseFields := map[string]any{
		"agent_id":       ts.agent.ID,
		"iteration":      iteration,
		"content_chars":  len(exec.response.Content),
		"tool_calls":     len(exec.response.ToolCalls),
		"reasoning":      exec.response.Reasoning,
		"target_channel": al.targetReasoningChannelID(ts.channel),
		"channel":        ts.channel,
	}
	if exec.response.Usage != nil {
		llmResponseFields["prompt_tokens"] = exec.response.Usage.PromptTokens
		llmResponseFields["completion_tokens"] = exec.response.Usage.CompletionTokens
		llmResponseFields["total_tokens"] = exec.response.Usage.TotalTokens
	}
	logger.DebugCF("agent", "LLM response", llmResponseFields)

	// No-tool-call path: steering check and direct response
	if len(exec.response.ToolCalls) == 0 {
		responseContent := exec.response.Content
		if responseContent == "" && exec.response.ReasoningContent != "" && ts.channel != "web" {
			responseContent = exec.response.ReasoningContent
		}
		if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
			logger.InfoCF("agent", "Steering arrived after direct LLM response; continuing turn",
				map[string]any{
					"agent_id":       ts.agent.ID,
					"iteration":      iteration,
					"steering_count": len(steerMsgs),
				})
			p.keepAnswerBeforeSteering(turnCtx, ts, exec, responseContent, reasoningContent)
			exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
			return ControlContinue, nil
		}

		exec.finalContent = responseContent
		logger.InfoCF("agent", "LLM response without tool calls (direct answer)",
			map[string]any{
				"agent_id":      ts.agent.ID,
				"iteration":     iteration,
				"content_chars": len(exec.finalContent),
			})
		return ControlBreak, nil
	}
	cancelConfiguredStreamingLLM(turnCtx, exec)

	// Tool-call path: normalize and prepare for tool execution
	exec.normalizedToolCalls = make([]providers.ToolCall, 0, len(exec.response.ToolCalls))
	for _, tc := range exec.response.ToolCalls {
		exec.normalizedToolCalls = append(exec.normalizedToolCalls, providers.NormalizeToolCall(tc))
	}

	toolNames := make([]string, 0, len(exec.normalizedToolCalls))
	for _, tc := range exec.normalizedToolCalls {
		toolNames = append(toolNames, tc.Name)
	}
	logger.InfoCF("agent", "LLM requested tool calls",
		map[string]any{
			"agent_id":  ts.agent.ID,
			"tools":     toolNames,
			"count":     len(exec.normalizedToolCalls),
			"iteration": iteration,
		})

	exec.allResponsesHandled = len(exec.normalizedToolCalls) > 0
	assistantMsg := providers.Message{
		Role:               "assistant",
		Content:            exec.response.Content,
		ModelName:          exec.llmModelName,
		RequestedSelection: exec.requestedSelection,
		ServedTarget:       exec.servedTarget,
		ServedIdentity:     exec.servedIdentity,
		ReasoningContent:   reasoningContent,
	}
	for _, tc := range exec.normalizedToolCalls {
		argumentsJSON, _ := json.Marshal(tc.Arguments)
		toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
			exec.response,
			tc,
			exec.messages,
		)
		extraContent := tc.ExtraContent
		if strings.TrimSpace(toolFeedbackExplanation) != "" {
			if extraContent == nil {
				extraContent = &providers.ExtraContent{}
			}
			extraContent.ToolFeedbackExplanation = toolFeedbackExplanation
		}
		thoughtSignature := ""
		if tc.Function != nil {
			thoughtSignature = tc.Function.ThoughtSignature
		}
		assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, providers.ToolCall{
			ID:   tc.ID,
			Type: "function",
			Name: tc.Name,
			Function: &providers.FunctionCall{
				Name:             tc.Name,
				Arguments:        string(argumentsJSON),
				ThoughtSignature: thoughtSignature,
			},
			ExtraContent:     extraContent,
			ThoughtSignature: thoughtSignature,
		})
	}
	exec.messages = append(exec.messages, assistantMsg)
	if !ts.opts.NoHistory {
		ts.agent.Sessions.AddFullMessage(ts.sessionKey, assistantMsg)
		ts.recordPersistedMessage(assistantMsg)
		ts.ingestMessage(turnCtx, al, assistantMsg)
	}
	if shouldPublishWebToolCallInterim {
		al.publishWebToolCallInterim(
			turnCtx,
			ts,
			exec.llmModelName,
			reasoningContent,
			exec.response.Content,
			assistantMsg.ToolCalls,
		)
	}

	return ControlToolLoop, nil
}

// keepAnswerBeforeSteering keeps a final answer that a steering message
// overtook. The model sees it and the session stores it ahead of the steering
// message, and the user gets it: through the stream that showed it, else as a
// message of its own (the turn's reply will be the answer to the steering).
func (p *Pipeline) keepAnswerBeforeSteering(
	ctx context.Context,
	ts *turnState,
	exec *turnExecution,
	content, reasoning string,
) {
	if strings.TrimSpace(content) == "" {
		cancelConfiguredStreamingLLM(ctx, exec)
		return
	}
	answer := providers.Message{
		Role:               "assistant",
		Content:            content,
		ModelName:          exec.llmModelName,
		RequestedSelection: exec.requestedSelection,
		ServedTarget:       exec.servedTarget,
		ServedIdentity:     exec.servedIdentity,
		ReasoningContent:   reasoning,
	}
	exec.messages = append(exec.messages, answer)
	if !ts.opts.NoHistory {
		ts.agent.Sessions.AddFullMessage(ts.sessionKey, answer)
		ts.recordPersistedMessage(answer)
		ts.ingestMessage(ctx, p.al, answer)
	}

	if exec.streamingPublisher != nil {
		if err := finalizeConfiguredStreamingLLM(ctx, ts, exec, content, nil); err != nil {
			logger.WarnCF("agent", "Failed to finish the stream of an answer overtaken by steering",
				map[string]any{"agent_id": ts.agent.ID, "channel": ts.channel, "error": err.Error()})
		}
		return
	}
	if p.al.bus == nil || ts.channel == "" || ts.chatID == "" ||
		(!ts.opts.SendResponse && !ts.opts.AllowInterimWebPublish) {
		return
	}
	if err := p.al.bus.PublishOutbound(ctx, outboundMessageForTurnWithOptions(ts, content, outboundTurnMessageOptions{
		modelName: exec.llmModelName,
	})); err != nil {
		logger.WarnCF("agent", "Failed to deliver an answer overtaken by steering",
			map[string]any{"agent_id": ts.agent.ID, "channel": ts.channel, "error": err.Error()})
	}
}

func restoreToolDefinition(
	current,
	available []providers.ToolDefinition,
	name string,
) []providers.ToolDefinition {
	for _, tool := range current {
		if strings.EqualFold(tool.Function.Name, name) {
			return current
		}
	}
	for _, tool := range available {
		if strings.EqualFold(tool.Function.Name, name) {
			return append(current, tool)
		}
	}
	return current
}

// applyBeforeLLMModelRewrite moves the turn onto the model a before_llm hook
// rewrote the request to. The rewrite names a selection — an exact target
// or a model route — which the turn resolves and runs on, targets and
// runtime settings included. A message that selected its own model keeps
// it: a hook cannot replace a model chosen for the message.
func (p *Pipeline) applyBeforeLLMModelRewrite(ts *turnState, exec *turnExecution) error {
	if p == nil || ts == nil || ts.agent == nil || exec == nil {
		return nil
	}
	selection := strings.TrimSpace(exec.llmModel)
	if selection == "" {
		return nil
	}
	if exec.requestedSelection != "" {
		return hookRewriteRejectedError(selection, exec.requestedSelection)
	}
	if selection == exec.hookSelection && len(exec.activeCandidates) > 0 && exec.inResolution(exec.activeCandidates[0]) {
		// The hook repeats its rewrite each iteration; the turn already runs
		// on it.
		exec.llmModel = exec.activeModel
		return nil
	}
	resolution, err := p.al.resolveTurnSelection(selection)
	if err != nil {
		return fmt.Errorf("hook-selected model %q is not available: %w", selection, err)
	}
	exec.useResolution(resolution)
	exec.hookSelection = selection
	candidates := append([]providers.FallbackCandidate(nil), resolution.Candidates...)
	provider, err := exec.providerFor(candidates[0])
	if err != nil {
		return fmt.Errorf("initialize hook-selected model %q: %w", candidates[0].DisplayName, err)
	}
	exec.activeCandidates = candidates
	exec.serveCandidate(candidates[0])
	exec.activeProvider = provider
	exec.llmModel = exec.activeModel
	exec.usedLight = false
	return nil
}

// hookRewriteRejectedError is the error of a hook rewriting the model of a
// message that selected its own.
func hookRewriteRejectedError(rewritten, selection string) error {
	return fmt.Errorf(
		"a before_llm hook changed the model to %q, but this message runs on the model it selected (%q); hooks cannot replace a model selected for a message",
		rewritten, selection,
	)
}

// maxLLMRetryAfterWait bounds how long a turn waits out an upstream's
// Retry-After before repeating a call; a longer wait ends the turn with the
// rate limit, whose message says when to try again.
const maxLLMRetryAfterWait = time.Minute

// ErrToolsRequired ends a turn whose model rejects tool calls when the
// agent's AGENT.md sets requireTools: true. Without that key the turn
// retries without tools instead.
var ErrToolsRequired = errors.New(
	"The selected model doesn't support tool calls, and this agent requires them. Choose a model that supports tool calling.",
)

// llmRetryReason names why a retryable call failed, from core's
// classification of its error.
func llmRetryReason(err error) string {
	failure := providers.DescribeFailure(err)
	switch {
	case failure.Class == core.ProviderErrorRateLimited:
		return "rate_limit"
	case failure.Timeout:
		return "timeout"
	case failure.Class == core.ProviderErrorTransport:
		return "network"
	case failure.StatusCode >= 500:
		return "server_error"
	}
	return "transient"
}

// contextOverflowPhrases are how providers word a request whose prompt does
// not fit the model's context window.
var contextOverflowPhrases = []string{
	"context_length_exceeded",
	"context_window_exceeded",
	"context length",
	"context window",
	"context_window",
	"maximum context",
	"prompt is too long",
	"prompt too long",
	"input is too long",
	"too many tokens",
	"request too large",
	"max message tokens",
	"input token count",
	"range of input length",
	"token limit",
}

// isContextOverflowError reports whether err says the prompt exceeded the
// model's context window. Only a request the provider rejected as such
// counts: core's classification rules out authentication, permission, rate
// limit, transport and configuration failures, and statuses other than
// 400, 413 and 422, whatever their message mentions.
func isContextOverflowError(err error) bool {
	if err == nil {
		return false
	}
	failure := providers.DescribeFailure(err)
	if failure.Disposition == core.DispositionRetryable || failure.AfterOutput || failure.Unavailable || failure.Timeout {
		return false
	}
	switch failure.Class {
	case core.ProviderErrorAuth, core.ProviderErrorForbidden, core.ProviderErrorRateLimited,
		core.ProviderErrorTransport, core.ProviderErrorConfiguration, core.ProviderErrorUnsupported:
		return false
	}
	switch failure.StatusCode {
	case 0, http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
	default:
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, phrase := range contextOverflowPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}

// sessionSnapshot is a session's stored history and summary at one moment.
type sessionSnapshot struct {
	key     string
	history []providers.Message
	summary string
}

func takeSessionSnapshot(store session.SessionStore, key string) *sessionSnapshot {
	if store == nil {
		return nil
	}
	return &sessionSnapshot{
		key:     key,
		history: append([]providers.Message(nil), store.GetHistory(key)...),
		summary: store.GetSummary(key),
	}
}

// restore puts the snapshot back. The turn stores nothing while it retries a
// call, so the snapshot is still the whole session.
func (s *sessionSnapshot) restore(store session.SessionStore) {
	if s == nil || store == nil {
		return
	}
	store.SetHistory(s.key, append([]providers.Message(nil), s.history...))
	store.SetSummary(s.key, s.summary)
	if err := store.Save(s.key); err != nil {
		logger.WarnCF("agent", "Failed to restore the session after a failed context retry", map[string]any{
			"session_key": s.key,
			"error":       err.Error(),
		})
	}
}
`n// errorChainText joins the messages of err and of every error it wraps.
// Provider errors keep a generic top-level message and carry the upstream
// reason (sanitized) as a cause, so wording checks must see the whole chain.
func errorChainText(err error) string {
	var parts []string
	seen := map[string]bool{}
	var walk func(error)
	walk = func(current error) {
		for current != nil {
			if message := current.Error(); !seen[message] {
				seen[message] = true
				parts = append(parts, message)
			}
			if joined, ok := current.(interface{ Unwrap() []error }); ok {
				for _, inner := range joined.Unwrap() {
					walk(inner)
				}
				return
			}
			current = errors.Unwrap(current)
		}
	}
	walk(err)
	return strings.Join(parts, " | ")
}

func isToolUnsupportedError(errMsg string) bool {
	return strings.Contains(errMsg, "no endpoints found that support tool use") ||
		strings.Contains(errMsg, "support tool use") ||
		strings.Contains(errMsg, "does not support tool") ||
		strings.Contains(errMsg, "tools are not supported") ||
		strings.Contains(errMsg, "tool use is not supported") ||
		strings.Contains(errMsg, "function calling is not supported") ||
		strings.Contains(errMsg, "tools is not supported") ||
		strings.Contains(errMsg, "tool_choice is not supported") ||
		strings.Contains(errMsg, "does not support function calling") ||
		strings.Contains(errMsg, "cannot call tools")
}
