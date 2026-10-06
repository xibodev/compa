// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xibodev/compa/v2/pkg/logger"
	"github.com/xibodev/compa/v2/pkg/providers"
)

// SetupTurn extracts the one-time initialization phase, returning a
// turnExecution populated with history, messages, and candidate selection.
// A turn runs on the message's own model selection when it has one, and on
// the agent's model otherwise; with neither it fails with a no-model error
// before anything is recorded.
func (p *Pipeline) SetupTurn(ctx context.Context, ts *turnState) (*turnExecution, error) {
	cfg := p.Cfg
	maxMediaSize := cfg.Agents.Defaults.GetMaxMediaSize()

	if ts.opts.InstanceResolution == nil && !ts.agent.hasModel() {
		return nil, ts.agent.noModelError()
	}
	if ts.opts.InstanceResolution != nil && len(ts.opts.InstanceResolution.Candidates) == 0 {
		return nil, fmt.Errorf("model selection %q resolved no targets", ts.opts.RequestedSelection)
	}

	var history []providers.Message
	var summary string
	if !ts.opts.NoHistory {
		if resp, err := p.ContextManager.Assemble(ctx, &AssembleRequest{
			SessionKey: ts.sessionKey,
			Budget:     ts.agent.ContextWindow,
			MaxTokens:  ts.agent.MaxTokens,
			AgentID:    ts.agent.ID,
		}); err == nil && resp != nil {
			history = resp.History
			summary = resp.Summary
		}
	}
	// A hard abort restores the turn agent's stored session, not the
	// assembled view: a context manager may assemble a trimmed one.
	ts.refreshRestorePointFromSession(ts.agent)

	contextualSkills := ts.activeSkills
	if ts.agent.ContextBuilder != nil {
		contextualSkills = ts.agent.ContextBuilder.ResolveActiveSkillsForContext(ts.activeSkills)
	}
	ts.recordSkillContextSnapshot(skillContextTriggerInitialBuild, contextualSkills)
	initialPromptReq := promptBuildRequestForTurn(ts, history, summary, ts.userMessage, ts.media, cfg)
	initialPromptReq.ActiveSkills = append([]string(nil), contextualSkills...)
	messages := ts.agent.ContextBuilder.BuildMessagesFromPrompt(initialPromptReq)
	currentTurnStart := len(messages)
	if strings.TrimSpace(ts.userMessage) != "" || len(ts.media) > 0 {
		currentTurnStart = len(messages) - 1
	}

	messages = resolveMediaRefs(messages, p.MediaStore, maxMediaSize, currentTurnStart)

	if !ts.opts.NoHistory {
		toolDefs := p.al.offeredToolDefs(ts)
		if isOverContextBudget(ts.agent.ContextWindow, messages, toolDefs, ts.agent.MaxTokens) {
			logger.WarnCF("agent", "Proactive compression: context budget exceeded before LLM call",
				map[string]any{"session_key": ts.sessionKey})
			if err := p.ContextManager.Compact(ctx, &CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonProactive,
				Budget:     ts.agent.ContextWindow,
				AgentID:    ts.agent.ID,
			}); err != nil {
				logger.WarnCF("agent", "Proactive compact failed", map[string]any{
					"session_key": ts.sessionKey,
					"error":       err.Error(),
				})
			}
			ts.refreshRestorePointFromSession(ts.agent)
			if resp, err := p.ContextManager.Assemble(ctx, &AssembleRequest{
				SessionKey: ts.sessionKey,
				Budget:     ts.agent.ContextWindow,
				MaxTokens:  ts.agent.MaxTokens,
				AgentID:    ts.agent.ID,
			}); err == nil && resp != nil {
				history = resp.History
				summary = resp.Summary
			}
			originalHistoryCount := len(history)
			var fit bool
			history, messages, fit = trimHistoryToFitContextWindow(
				history,
				func(trimmedHistory []providers.Message) []providers.Message {
					rebuildPromptReq := promptBuildRequestForTurn(
						ts,
						trimmedHistory,
						summary,
						ts.userMessage,
						ts.media,
						cfg,
					)
					rebuildPromptReq.ActiveSkills = append([]string(nil), contextualSkills...)
					rebuilt := ts.agent.ContextBuilder.BuildMessagesFromPrompt(rebuildPromptReq)
					rebuiltCurrentTurnStart := len(rebuilt)
					if strings.TrimSpace(ts.userMessage) != "" || len(ts.media) > 0 {
						rebuiltCurrentTurnStart = len(rebuilt) - 1
					}
					return resolveMediaRefs(rebuilt, p.MediaStore, maxMediaSize, rebuiltCurrentTurnStart)
				},
				ts.agent.ContextWindow,
				toolDefs,
				ts.agent.MaxTokens,
			)
			if dropped := originalHistoryCount - len(history); dropped > 0 {
				logger.WarnCF("agent", "Trimmed rebuilt history after proactive compaction", map[string]any{
					"session_key":     ts.sessionKey,
					"dropped_msgs":    dropped,
					"remaining_msgs":  len(history),
					"context_window":  ts.agent.ContextWindow,
					"max_tokens":      ts.agent.MaxTokens,
					"still_overlimit": !fit,
				})
			} else if !fit {
				logger.WarnCF("agent", "Context still exceeds budget "+
					"after proactive compaction rebuild", map[string]any{
					"session_key":    ts.sessionKey,
					"history_msgs":   len(history),
					"context_window": ts.agent.ContextWindow,
					"max_tokens":     ts.agent.MaxTokens,
				})
			}
		}
	}

	if !ts.opts.NoHistory && (strings.TrimSpace(ts.userMessage) != "" || len(ts.media) > 0) {
		rootMsg := userPromptMessage(ts.userMessage, ts.media)
		rootMsg.RequestedSelection = ts.opts.RequestedSelection
		if len(rootMsg.Media) > 0 || rootMsg.RequestedSelection != "" {
			ts.agent.Sessions.AddFullMessage(ts.sessionKey, rootMsg)
		} else {
			ts.agent.Sessions.AddMessage(ts.sessionKey, rootMsg.Role, rootMsg.Content)
		}
		ts.recordPersistedMessage(rootMsg)
		ts.ingestMessage(ctx, p.al, rootMsg)
	}

	exec := newTurnExecution(
		ts.agent,
		ts.opts,
		history,
		summary,
		messages,
	)
	exec.currentTurnStart = currentTurnStart
	var (
		activeCandidates []providers.FallbackCandidate
		activeModel      string
		usedLight        bool
	)
	if ts.opts.InstanceResolution != nil {
		exec.useResolution(ts.opts.InstanceResolution)
		exec.requestedSelection = ts.opts.RequestedSelection
		activeCandidates = append([]providers.FallbackCandidate(nil), ts.opts.InstanceResolution.Candidates...)
		activeModel = activeCandidates[0].Model
	} else {
		activeCandidates, activeModel, usedLight = p.al.selectCandidates(ts.agent, ts.userMessage, messages)
	}
	primary := activeCandidates[0]
	activeProvider, err := exec.providerFor(primary)
	if err != nil {
		return nil, fmt.Errorf("initialize model %q: %w", primary.DisplayName, err)
	}
	exec.activeCandidates = activeCandidates
	exec.activeModel = activeModel
	exec.activeCallSpec = exec.configFor(primary)
	exec.llmModelName = primary.DisplayName
	exec.activeProvider = activeProvider
	exec.usedLight = usedLight
	if exec.requestedSelection != "" {
		exec.servedTarget = primary.DisplayName
		exec.servedIdentity = primary.StableKey()
	}

	return exec, nil
}
