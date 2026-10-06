// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xibodev/compa/v2/pkg/approval"
	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/constants"
	runtimeevents "github.com/xibodev/compa/v2/pkg/events"
	"github.com/xibodev/compa/v2/pkg/logger"
	"github.com/xibodev/compa/v2/pkg/providers"
	"github.com/xibodev/compa/v2/pkg/tools"
	"github.com/xibodev/compa/v2/pkg/utils"
)

// Remaining tool calls in a batch are skipped once a steering message is queued
// for the turn, so the model sees the user's new input before doing more work.
const (
	steeringSkipReason  = "queued user steering message"
	steeringSkipMessage = "Skipped due to queued user message."
)

func toolErrorSummary(result *tools.ToolResult) string {
	if result == nil || !result.IsError {
		return ""
	}
	content := strings.TrimSpace(result.ContentForLLM())
	if content == "" && result.Err != nil {
		content = strings.TrimSpace(result.Err.Error())
	}
	return utils.Truncate(content, 200)
}

func inferSkillNamesFromToolCall(ts *turnState, toolName string, toolArgs map[string]any) []string {
	if ts == nil || toolName != "read_file" {
		return nil
	}

	rawPath, ok := toolArgs["path"].(string)
	if !ok {
		return nil
	}
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return nil
	}

	cleanPath := filepath.Clean(path)
	if !filepath.IsAbs(cleanPath) {
		cleanPath = filepath.Join(ts.workspace, cleanPath)
	}
	if filepath.Base(cleanPath) != "SKILL.md" {
		return nil
	}

	var roots []string
	if ts.agent != nil && ts.agent.ContextBuilder != nil {
		roots = ts.agent.ContextBuilder.skillRoots()
	}
	if len(roots) == 0 && strings.TrimSpace(ts.workspace) != "" {
		roots = []string{filepath.Join(ts.workspace, "skills")}
	}

	found := make(map[string]struct{})
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		rel, err := filepath.Rel(filepath.Clean(root), cleanPath)
		if err != nil {
			continue
		}
		if rel == "." || rel == "" || strings.HasPrefix(rel, "..") {
			continue
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) != 2 || parts[1] != "SKILL.md" {
			continue
		}

		skillName := strings.TrimSpace(parts[0])
		if skillName == "" {
			continue
		}
		if ts.agent != nil && ts.agent.ContextBuilder != nil {
			if canonical, ok := ts.agent.ContextBuilder.ResolveSkillName(skillName); ok {
				skillName = canonical
			}
		}
		found[skillName] = struct{}{}
	}

	if len(found) == 0 {
		return nil
	}

	names := make([]string, 0, len(found))
	for skillName := range found {
		names = append(names, skillName)
	}
	sort.Strings(names)
	return names
}

// ExecuteTools executes the tool loop, handling BeforeTool/AfterTool hooks,
// the approval policy, tool execution with async callbacks, media delivery,
// and steering injection.
// Returns ToolControl indicating what the coordinator should do next:
//   - ToolControlContinue: all tool results handled, pendingMessages or steering exists, continue turn
//   - ToolControlBreak: tool loop exited, proceed to coordinator's hardAbort/finalContent/finalize
func (p *Pipeline) ExecuteTools(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	iteration int,
) ToolControl {
	al := p.al
	normalizedToolCalls := exec.normalizedToolCalls

	ts.setPhase(TurnPhaseTools)
	messages := exec.messages
	handledAttachments := make([]providers.Attachment, 0)
	var subTurnResults []providers.Message

toolLoop:
	for i, tc := range normalizedToolCalls {
		if ts.hardAbortRequested() {
			exec.abortedByHardAbort = true
			return ToolControlBreak
		}

		toolName := tc.Name
		toolArgs := cloneStringAnyMap(tc.Arguments)
		denyByTurnProfile := func() bool {
			if turnProfileToolAllowed(ts.profile, toolName) {
				return false
			}
			exec.allResponsesHandled = false
			denyContent := fmt.Sprintf("Tool %q is not allowed by the active turn profile.", toolName)
			al.emitEvent(
				runtimeevents.KindAgentToolExecSkipped,
				ts.eventMeta("runTurn", "turn.tool.skipped"),
				ToolExecSkippedPayload{
					Tool:   toolName,
					Reason: denyContent,
				},
			)
			deniedMsg := providers.Message{
				Role:       "tool",
				Content:    denyContent,
				ToolCallID: tc.ID,
			}
			messages = append(messages, deniedMsg)
			if !ts.opts.NoHistory {
				ts.agent.Sessions.AddFullMessage(ts.sessionKey, deniedMsg)
				ts.recordPersistedMessage(deniedMsg)
			}
			return true
		}

		if denyByTurnProfile() {
			continue
		}

		if al.hooks != nil {
			toolReq, decision := al.hooks.BeforeTool(turnCtx, &ToolCallHookRequest{
				Meta:      ts.eventMeta("runTurn", "turn.tool.before"),
				Context:   cloneTurnContext(ts.turnCtx),
				Tool:      toolName,
				Arguments: toolArgs,
			})
			switch decision.normalizedAction() {
			case HookActionContinue, HookActionModify:
				if toolReq != nil {
					toolName = toolReq.Tool
					toolArgs = toolReq.Arguments
				}
			case HookActionRespond:
				if toolReq != nil && toolReq.HookResult != nil {
					hookResult := toolReq.HookResult

					argsJSON, _ := json.Marshal(toolArgs)
					argsPreview := utils.Truncate(string(argsJSON), 200)
					logger.InfoCF("agent", fmt.Sprintf("Tool call (hook respond): %s(%s)", toolName, argsPreview),
						map[string]any{
							"agent_id":  ts.agent.ID,
							"tool":      toolName,
							"iteration": iteration,
						})

					al.emitEvent(
						runtimeevents.KindAgentToolExecStart,
						ts.eventMeta("runTurn", "turn.tool.start"),
						ToolExecStartPayload{
							Tool:      toolName,
							Arguments: cloneEventArguments(toolArgs),
						},
					)

					if shouldPublishToolFeedback(p.Cfg, ts) && ts.channel != "web" {
						toolFeedbackMaxLen := p.Cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength()
						toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
							exec.response,
							tc,
							messages,
						)
						feedbackMsg := utils.FormatToolFeedbackMessage(
							toolName,
							toolFeedbackExplanation,
							toolFeedbackArgsPreview(toolArgs, toolFeedbackMaxLen),
						)
						fbCtx, fbCancel := context.WithTimeout(turnCtx, 3*time.Second)
						_ = al.bus.PublishOutbound(fbCtx, outboundMessageForTurnWithOptions(
							ts,
							feedbackMsg,
							outboundTurnMessageOptions{kind: messageKindToolFeedback},
						))
						fbCancel()
					}

					toolDuration := time.Duration(0)

					shouldSendForUser := !hookResult.Silent && hookResult.ForUser != "" &&
						(ts.opts.SendResponse || hookResult.ResponseHandled)
					if shouldSendForUser {
						// Like a tool's own output: to the turn's chat, keeping
						// its account and topic.
						al.bus.PublishOutbound(ctx, outboundMessageForTurn(ts, hookResult.ForUser))
					}

					if len(hookResult.Media) > 0 && hookResult.ResponseHandled {
						parts := make([]bus.MediaPart, 0, len(hookResult.Media))
						for _, ref := range hookResult.Media {
							part := bus.MediaPart{Ref: ref}
							if p.MediaStore != nil {
								if _, meta, err := p.MediaStore.ResolveWithMeta(ref); err == nil {
									part.Filename = meta.Filename
									part.ContentType = meta.ContentType
									part.Type = inferMediaType(meta.Filename, meta.ContentType)
								}
							}
							parts = append(parts, part)
						}
						outboundMedia := bus.OutboundMediaMessage{
							Channel: ts.channel,
							ChatID:  ts.chatID,
							Context: outboundContextFromInbound(
								ts.opts.Dispatch.InboundContext,
								ts.channel,
								ts.chatID,
								ts.opts.Dispatch.ReplyToMessageID(),
							),
							AgentID:    ts.agent.ID,
							SessionKey: ts.sessionKey,
							Scope:      outboundScopeFromSessionScope(ts.opts.Dispatch.SessionScope),
							Parts:      parts,
						}
						if p.ChannelManager != nil && ts.channel != "" && !constants.IsInternalChannel(ts.channel) {
							if err := p.ChannelManager.SendMedia(ctx, outboundMedia); err != nil {
								logger.WarnCF("agent", "Failed to deliver hook media",
									map[string]any{
										"agent_id": ts.agent.ID,
										"tool":     toolName,
										"channel":  ts.channel,
										"chat_id":  ts.chatID,
										"error":    err.Error(),
									})
								hookResult.IsError = true
								hookResult.ForLLM = fmt.Sprintf("failed to deliver attachment: %v", err)
							} else {
								handledAttachments = append(
									handledAttachments,
									buildProviderAttachments(p.MediaStore, hookResult.Media)...,
								)
							}
						} else if al.bus != nil {
							al.bus.PublishOutboundMedia(ctx, outboundMedia)
							hookResult.ResponseHandled = false
						}
					}

					if !hookResult.ResponseHandled {
						exec.allResponsesHandled = false
					}

					contentForLLM := hookResult.ContentForLLM()
					if p.Cfg.Tools.IsFilterSensitiveDataEnabled() {
						contentForLLM = p.Cfg.FilterSensitiveData(contentForLLM)
					}

					var toolResultMedia []string
					if len(hookResult.Media) > 0 && !hookResult.ResponseHandled {
						hookResult.ArtifactTags = buildArtifactTags(p.MediaStore, hookResult.Media)
						contentForLLM = hookResult.ContentForLLM()
						if p.Cfg.Tools.IsFilterSensitiveDataEnabled() {
							contentForLLM = p.Cfg.FilterSensitiveData(contentForLLM)
						}
						toolResultMedia = append(toolResultMedia, hookResult.Media...)
					}
					toolResultMsg := toolResultPromptMessage(contentForLLM, tc.ID, toolResultMedia)

					al.emitEvent(
						runtimeevents.KindAgentToolExecEnd,
						ts.eventMeta("runTurn", "turn.tool.end"),
						ToolExecEndPayload{
							Tool:       toolName,
							Duration:   toolDuration,
							ForLLMLen:  len(contentForLLM),
							ForUserLen: len(hookResult.ForUser),
							IsError:    hookResult.IsError,
							Async:      hookResult.Async,
						},
					)
					ts.recordToolExecution(
						toolName,
						!hookResult.IsError,
						toolErrorSummary(hookResult),
						inferSkillNamesFromToolCall(ts, toolName, toolArgs),
					)

					messages = append(messages, toolResultMsg)
					if !ts.opts.NoHistory {
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, toolResultMsg)
						ts.recordPersistedMessage(toolResultMsg)
						ts.ingestMessage(turnCtx, al, toolResultMsg)
					}

					if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
						exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
					}

					if len(exec.pendingMessages) > 0 {
						remaining := len(normalizedToolCalls) - i - 1
						if remaining > 0 {
							logger.InfoCF("agent", "Turn checkpoint: skipping remaining tools after hook respond",
								map[string]any{
									"agent_id":  ts.agent.ID,
									"completed": i + 1,
									"skipped":   remaining,
									"reason":    steeringSkipReason,
								})
							for j := i + 1; j < len(normalizedToolCalls); j++ {
								skippedTC := normalizedToolCalls[j]
								al.emitEvent(
									runtimeevents.KindAgentToolExecSkipped,
									ts.eventMeta("runTurn", "turn.tool.skipped"),
									ToolExecSkippedPayload{
										Tool:   skippedTC.Name,
										Reason: steeringSkipReason,
									},
								)
								skippedMsg := providers.Message{
									Role:       "tool",
									Content:    steeringSkipMessage,
									ToolCallID: skippedTC.ID,
								}
								messages = append(messages, skippedMsg)
								if !ts.opts.NoHistory {
									ts.agent.Sessions.AddFullMessage(ts.sessionKey, skippedMsg)
									ts.recordPersistedMessage(skippedMsg)
								}
							}
						}
						break toolLoop
					}

					if msg, ok := p.takeSubTurnResult(ts); ok {
						subTurnResults = append(subTurnResults, msg)
					}

					continue
				}
				logger.WarnCF("agent", "Hook returned respond action but no HookResult provided",
					map[string]any{
						"agent_id": ts.agent.ID,
						"tool":     toolName,
						"action":   "respond",
					})
			case HookActionDenyTool:
				exec.allResponsesHandled = false
				denyContent := hookDeniedToolContent("Tool execution denied by hook", decision.Reason)
				al.emitEvent(
					runtimeevents.KindAgentToolExecSkipped,
					ts.eventMeta("runTurn", "turn.tool.skipped"),
					ToolExecSkippedPayload{
						Tool:   toolName,
						Reason: denyContent,
					},
				)
				deniedMsg := providers.Message{
					Role:       "tool",
					Content:    denyContent,
					ToolCallID: tc.ID,
				}
				messages = append(messages, deniedMsg)
				if !ts.opts.NoHistory {
					ts.agent.Sessions.AddFullMessage(ts.sessionKey, deniedMsg)
					ts.recordPersistedMessage(deniedMsg)
				}
				continue
			case HookActionAbortTurn:
				exec.abortedByHook = true
				return ToolControlBreak
			case HookActionHardAbort:
				_ = ts.requestHardAbort()
				exec.abortedByHardAbort = true
				return ToolControlBreak
			}
		}

		verdict := al.authorizeToolCall(turnCtx, ts, tc.ID, toolName, toolArgs)
		if !verdict.allowed {
			exec.allResponsesHandled = false
			al.emitEvent(
				runtimeevents.KindAgentToolExecSkipped,
				ts.eventMeta("runTurn", "turn.tool.skipped"),
				ToolExecSkippedPayload{
					Tool:   toolName,
					Reason: verdict.denyContent,
				},
			)
			deniedMsg := providers.Message{
				Role:       "tool",
				Content:    verdict.denyContent,
				ToolCallID: tc.ID,
			}
			messages = append(messages, deniedMsg)
			if !ts.opts.NoHistory {
				ts.agent.Sessions.AddFullMessage(ts.sessionKey, deniedMsg)
				ts.recordPersistedMessage(deniedMsg)
			}
			continue
		}

		if denyByTurnProfile() {
			continue
		}

		argsJSON, _ := json.Marshal(toolArgs)
		argsPreview := utils.Truncate(string(argsJSON), 200)
		logger.InfoCF("agent", fmt.Sprintf("Tool call: %s(%s)", toolName, argsPreview),
			map[string]any{
				"agent_id":  ts.agent.ID,
				"tool":      toolName,
				"iteration": iteration,
			})
		al.emitEvent(
			runtimeevents.KindAgentToolExecStart,
			ts.eventMeta("runTurn", "turn.tool.start"),
			ToolExecStartPayload{
				Tool:      toolName,
				Arguments: cloneEventArguments(toolArgs),
			},
		)

		if shouldPublishToolFeedback(p.Cfg, ts) && ts.channel != "web" {
			toolFeedbackMaxLen := p.Cfg.Agents.Defaults.GetToolFeedbackMaxArgsLength()
			toolFeedbackExplanation := toolFeedbackExplanationForToolCall(
				exec.response,
				tc,
				messages,
			)
			feedbackMsg := utils.FormatToolFeedbackMessage(
				toolName,
				toolFeedbackExplanation,
				toolFeedbackArgsPreview(toolArgs, toolFeedbackMaxLen),
			)
			fbCtx, fbCancel := context.WithTimeout(turnCtx, 3*time.Second)
			_ = al.bus.PublishOutbound(fbCtx, outboundMessageForTurnWithOptions(
				ts,
				feedbackMsg,
				outboundTurnMessageOptions{kind: messageKindToolFeedback},
			))
			fbCancel()
		}

		toolCallID := tc.ID
		// An async tool's result comes back as a turn of this session; see
		// handleAsyncToolResult.
		asyncCallback := al.asyncToolCallback(turnCtx, ts, toolName, iteration)
		toolStart := time.Now()
		execCtx := tools.WithToolInboundContext(
			turnCtx,
			ts.channel,
			ts.chatID,
			ts.opts.Dispatch.MessageID(),
			ts.opts.Dispatch.ReplyToMessageID(),
		)
		execCtx = tools.WithToolSessionContext(
			execCtx,
			ts.agent.ID,
			ts.sessionKey,
			ts.opts.Dispatch.SessionScope,
		)
		if verdict.approved {
			execCtx = approval.WithApproved(execCtx)
		}
		if hidden := al.hiddenTools(ts); hidden != nil {
			// Tool discovery doesn't offer what the turn is not offered.
			execCtx = tools.WithHiddenTools(execCtx, hidden)
		}
		toolResult := executeToolWithTimeout(
			execCtx,
			ts.agent.Tools,
			toolName,
			toolArgs,
			ts.channel,
			ts.chatID,
			asyncCallback,
		)
		toolDuration := time.Since(toolStart)

		if ts.hardAbortRequested() {
			exec.abortedByHardAbort = true
			return ToolControlBreak
		}

		if al.hooks != nil {
			toolResp, decision := al.hooks.AfterTool(turnCtx, &ToolResultHookResponse{
				Meta:      ts.eventMeta("runTurn", "turn.tool.after"),
				Context:   cloneTurnContext(ts.turnCtx),
				Tool:      toolName,
				Arguments: toolArgs,
				Result:    toolResult,
				Duration:  toolDuration,
			})
			switch decision.normalizedAction() {
			case HookActionContinue, HookActionModify:
				if toolResp != nil {
					if toolResp.Tool != "" {
						toolName = toolResp.Tool
					}
					if toolResp.Result != nil {
						toolResult = toolResp.Result
					}
				}
			case HookActionAbortTurn:
				exec.abortedByHook = true
				return ToolControlBreak
			case HookActionHardAbort:
				_ = ts.requestHardAbort()
				exec.abortedByHardAbort = true
				return ToolControlBreak
			}
		}

		if toolResult == nil {
			toolResult = tools.ErrorResult("hook returned nil tool result")
		}

		if len(toolResult.Media) > 0 && toolResult.ResponseHandled {
			parts := make([]bus.MediaPart, 0, len(toolResult.Media))
			for _, ref := range toolResult.Media {
				part := bus.MediaPart{Ref: ref}
				if p.MediaStore != nil {
					if _, meta, err := p.MediaStore.ResolveWithMeta(ref); err == nil {
						part.Filename = meta.Filename
						part.ContentType = meta.ContentType
						part.Type = inferMediaType(meta.Filename, meta.ContentType)
					}
				}
				parts = append(parts, part)
			}
			outboundMedia := bus.OutboundMediaMessage{
				Channel: ts.channel,
				ChatID:  ts.chatID,
				Context: outboundContextFromInbound(
					ts.opts.Dispatch.InboundContext,
					ts.channel,
					ts.chatID,
					ts.opts.Dispatch.ReplyToMessageID(),
				),
				AgentID:    ts.agent.ID,
				SessionKey: ts.sessionKey,
				Scope:      outboundScopeFromSessionScope(ts.opts.Dispatch.SessionScope),
				Parts:      parts,
			}
			if p.ChannelManager != nil && ts.channel != "" && !constants.IsInternalChannel(ts.channel) {
				if err := p.ChannelManager.SendMedia(ctx, outboundMedia); err != nil {
					logger.WarnCF("agent", "Failed to deliver handled tool media",
						map[string]any{
							"agent_id": ts.agent.ID,
							"tool":     toolName,
							"channel":  ts.channel,
							"chat_id":  ts.chatID,
							"error":    err.Error(),
						})
					toolResult = tools.ErrorResult(fmt.Sprintf("failed to deliver attachment: %v", err)).WithError(err)
				} else {
					handledAttachments = append(
						handledAttachments,
						buildProviderAttachments(p.MediaStore, toolResult.Media)...,
					)
				}
			} else if al.bus != nil {
				al.bus.PublishOutboundMedia(ctx, outboundMedia)
				toolResult.ResponseHandled = false
			}
		}

		if len(toolResult.Media) > 0 && !toolResult.ResponseHandled {
			toolResult.ArtifactTags = buildArtifactTags(p.MediaStore, toolResult.Media)
		}

		if !toolResult.ResponseHandled {
			exec.allResponsesHandled = false
		}

		shouldSendForUser := !toolResult.Silent &&
			toolResult.ForUser != "" &&
			(ts.opts.SendResponse || toolResult.ResponseHandled)
		if shouldSendForUser {
			al.bus.PublishOutbound(ctx, outboundMessageForTurn(ts, toolResult.ForUser))
			logger.DebugCF("agent", "Sent tool result to user",
				map[string]any{
					"tool":        toolName,
					"content_len": len(toolResult.ForUser),
				})
		}
		contentForLLM := toolResult.ContentForLLM()

		if p.Cfg.Tools.IsFilterSensitiveDataEnabled() {
			contentForLLM = p.Cfg.FilterSensitiveData(contentForLLM)
		}

		var toolResultMedia []string
		if len(toolResult.Media) > 0 && !toolResult.ResponseHandled {
			toolResultMedia = append(toolResultMedia, toolResult.Media...)
		}
		toolResultMsg := toolResultPromptMessage(contentForLLM, toolCallID, toolResultMedia)
		al.emitEvent(
			runtimeevents.KindAgentToolExecEnd,
			ts.eventMeta("runTurn", "turn.tool.end"),
			ToolExecEndPayload{
				Tool:       toolName,
				Duration:   toolDuration,
				ForLLMLen:  len(contentForLLM),
				ForUserLen: len(toolResult.ForUser),
				IsError:    toolResult.IsError,
				Async:      toolResult.Async,
			},
		)
		ts.recordToolExecution(
			toolName,
			!toolResult.IsError,
			toolErrorSummary(toolResult),
			inferSkillNamesFromToolCall(ts, toolName, toolArgs),
		)
		messages = append(messages, toolResultMsg)
		if !ts.opts.NoHistory {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, toolResultMsg)
			ts.recordPersistedMessage(toolResultMsg)
			ts.ingestMessage(turnCtx, al, toolResultMsg)
		}

		if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
			exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
		}

		if len(exec.pendingMessages) > 0 {
			remaining := len(normalizedToolCalls) - i - 1
			if remaining > 0 {
				logger.InfoCF("agent", "Turn checkpoint: skipping remaining tools",
					map[string]any{
						"agent_id":  ts.agent.ID,
						"completed": i + 1,
						"skipped":   remaining,
						"reason":    steeringSkipReason,
					})
				for j := i + 1; j < len(normalizedToolCalls); j++ {
					skippedTC := normalizedToolCalls[j]
					al.emitEvent(
						runtimeevents.KindAgentToolExecSkipped,
						ts.eventMeta("runTurn", "turn.tool.skipped"),
						ToolExecSkippedPayload{
							Tool:   skippedTC.Name,
							Reason: steeringSkipReason,
						},
					)
					skippedMsg := providers.Message{
						Role:       "tool",
						Content:    steeringSkipMessage,
						ToolCallID: skippedTC.ID,
					}
					messages = append(messages, skippedMsg)
					if !ts.opts.NoHistory {
						ts.agent.Sessions.AddFullMessage(ts.sessionKey, skippedMsg)
						ts.recordPersistedMessage(skippedMsg)
					}
				}
			}
			break toolLoop
		}

		if msg, ok := p.takeSubTurnResult(ts); ok {
			subTurnResults = append(subTurnResults, msg)
		}
	}

	// Sub-turn results follow the whole tool-result block: a user message
	// between the results of one batch of tool calls is a sequence providers
	// reject.
	for _, msg := range subTurnResults {
		messages = append(messages, msg)
		if !ts.opts.NoHistory {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, msg)
			ts.recordPersistedMessage(msg)
		}
	}

	exec.messages = messages

	// Continue if pending steering exists (regardless of allResponsesHandled).
	// This covers the case where tools were partially executed and skipped due to steering,
	// but one tool had ResponseHandled=false (so allResponsesHandled=false).
	if len(exec.pendingMessages) > 0 {
		logger.InfoCF("agent", "Pending steering after partial tool execution; continuing turn",
			map[string]any{
				"agent_id":            ts.agent.ID,
				"pending_count":       len(exec.pendingMessages),
				"allResponsesHandled": exec.allResponsesHandled,
			})
		exec.allResponsesHandled = false
		return ToolControlContinue
	}

	// Poll for newly arrived steering
	if steerMsgs := al.dequeueSteeringMessagesForScope(ts.sessionKey); len(steerMsgs) > 0 {
		logger.InfoCF("agent", "Steering arrived after tool delivery; continuing turn",
			map[string]any{
				"agent_id":       ts.agent.ID,
				"steering_count": len(steerMsgs),
			})
		exec.pendingMessages = append(exec.pendingMessages, steerMsgs...)
		exec.allResponsesHandled = false
		return ToolControlContinue
	}

	// No pending steering: finalize or break depending on allResponsesHandled
	if exec.allResponsesHandled {
		summaryMsg := providers.Message{
			Role:        "assistant",
			Content:     handledToolResponseSummary,
			Attachments: append([]providers.Attachment(nil), handledAttachments...),
		}
		if !ts.opts.NoHistory {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, summaryMsg)
			ts.recordPersistedMessage(summaryMsg)
			ts.ingestMessage(turnCtx, al, summaryMsg)
			if err := ts.agent.Sessions.Save(ts.sessionKey); err != nil {
				logger.WarnCF("agent", "Failed to save session after tool delivery",
					map[string]any{
						"agent_id": ts.agent.ID,
						"error":    err.Error(),
					})
			}
		}
		if !ts.opts.NoHistory && ts.opts.EnableSummary {
			p.ContextManager.Compact(turnCtx, &CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonSummarize,
				Budget:     ts.agent.ContextWindow,
				AgentID:    ts.agent.ID,
			})
		}
		ts.setPhase(TurnPhaseCompleted)
		ts.setFinalContent("")
		if p.ChannelManager != nil && ts.channel != "" {
			p.ChannelManager.DismissToolFeedback(ctx, ts.channel, ts.chatID, ts.opts.Dispatch.InboundContext)
		}
		logger.InfoCF("agent", "Tool output satisfied delivery; ending turn without follow-up LLM",
			map[string]any{
				"agent_id":   ts.agent.ID,
				"iteration":  iteration,
				"tool_count": len(normalizedToolCalls),
			})
		return ToolControlBreak
	}

	// allResponsesHandled=false and no pending steering: continue so coordinator
	// makes another LLM call. The tool result is in messages and the LLM will
	// return it as finalContent in the next iteration. Tools this session
	// unlocked through discovery count down one round; other sessions' don't.
	ts.agent.Tools.TickTTLForSession(ts.sessionKey)
	logger.DebugCF("agent", "TTL tick after tool execution", map[string]any{
		"agent_id": ts.agent.ID, "iteration": iteration,
	})
	return ToolControlContinue
}
