package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	runtimeevents "github.com/xibodev/compa/v3/pkg/events"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/session"
)

// defaultContextManagerName selects the built-in context manager in config.
// An empty context_manager setting selects it too.
const defaultContextManagerName = "default"

// defaultContextManager keeps conversation context in the agent session store
// and bounds it with summarization and emergency compression. It is used when
// no other ContextManager is configured.
type defaultContextManager struct {
	al          *AgentLoop
	summarizing sync.Map // dedup for async Compact (post-turn)
}

// agentFor returns the agent whose session store holds a session: the
// agent of the turn that asks, else the agent that owns the session. A routed
// agent keeps its history in its own store, never in the default agent's.
func (m *defaultContextManager) agentFor(agentID, sessionKey string) *AgentInstance {
	if strings.TrimSpace(agentID) != "" {
		if registry := m.al.GetRegistry(); registry != nil {
			if agent, ok := registry.GetAgent(agentID); ok && agent != nil {
				return agent
			}
		}
	}
	return m.al.agentForSession(sessionKey)
}

func (m *defaultContextManager) Assemble(_ context.Context, req *AssembleRequest) (*AssembleResponse, error) {
	// Read history from the session store and return it as-is.
	// Budget enforcement happens in BuildMessagesFromPrompt caller via
	// isOverContextBudget + forceCompression.
	agent := m.agentFor(req.AgentID, req.SessionKey)
	if agent == nil || agent.Sessions == nil {
		return &AssembleResponse{}, nil
	}
	history := agent.Sessions.GetHistory(req.SessionKey)
	summary := agent.Sessions.GetSummary(req.SessionKey)
	return &AssembleResponse{
		History: history,
		Summary: summary,
	}, nil
}

func (m *defaultContextManager) Compact(_ context.Context, req *CompactRequest) error {
	switch req.Reason {
	case ContextCompressReasonProactive, ContextCompressReasonRetry:
		// Sync emergency compression — budget exceeded.
		if result, ok := m.forceCompression(req.AgentID, req.SessionKey); ok {
			m.al.emitEvent(
				runtimeevents.KindAgentContextCompress,
				m.al.newTurnEventScope("", req.SessionKey, nil).meta(0, "forceCompression", "turn.context.compress"),
				ContextCompressPayload{
					Reason:            req.Reason,
					DroppedMessages:   result.DroppedMessages,
					RemainingMessages: result.RemainingMessages,
				},
			)
		}
	case ContextCompressReasonSummarize:
		m.maybeSummarize(req.AgentID, req.SessionKey)
	}
	return nil
}

func (m *defaultContextManager) Ingest(_ context.Context, _ *IngestRequest) error {
	// No-op: messages are already persisted by the session store.
	return nil
}

func (m *defaultContextManager) Clear(_ context.Context, sessionKey string) error {
	// Routed (non-default) agents keep history in their own session store,
	// so resolve the owning agent instead of assuming the default one.
	agent := m.al.agentForSession(sessionKey)
	if agent == nil || agent.Sessions == nil {
		return fmt.Errorf("sessions not initialized")
	}
	agent.Sessions.SetHistory(sessionKey, []providers.Message{})
	agent.Sessions.SetSummary(sessionKey, "")
	return agent.Sessions.Save(sessionKey)
}

// maybeSummarize triggers summarization if the session history exceeds thresholds.
// It runs asynchronously in a goroutine.
func (m *defaultContextManager) maybeSummarize(agentID, sessionKey string) {
	agent := m.agentFor(agentID, sessionKey)
	if agent == nil || agent.Sessions == nil {
		return
	}

	newHistory := agent.Sessions.GetHistory(sessionKey)
	tokenEstimate := m.estimateTokens(newHistory)
	threshold := agent.ContextWindow * agent.SummarizeTokenPercent / 100

	if len(newHistory) > agent.SummarizeMessageThreshold || tokenEstimate > threshold {
		summarizeKey := agent.ID + ":" + sessionKey
		if _, loading := m.summarizing.LoadOrStore(summarizeKey, true); !loading {
			go func() {
				defer m.summarizing.Delete(summarizeKey)
				defer func() {
					if r := recover(); r != nil {
						logger.WarnCF("agent", "Summarization panic recovered", map[string]any{
							"session_key": sessionKey,
							"panic":       r,
						})
					}
				}()
				logger.Debug("Memory threshold reached. Optimizing conversation history...")
				m.summarizeSession(agent, sessionKey)
			}()
		}
	}
}

type compressionResult struct {
	DroppedMessages   int
	RemainingMessages int
}

// forceCompression aggressively reduces context when the limit is hit.
// It drops the oldest ~50% of Turns (a Turn is a complete user→LLM→response
// cycle, as defined in #1316), so tool-call sequences are never split. The
// messages the active turn of the session has persisted — its prompt, tool
// calls and tool results — are never dropped: only what precedes them is.
func (m *defaultContextManager) forceCompression(agentID, sessionKey string) (compressionResult, bool) {
	agent := m.agentFor(agentID, sessionKey)
	if agent == nil || agent.Sessions == nil {
		return compressionResult{}, false
	}

	history := agent.Sessions.GetHistory(sessionKey)
	if len(history) <= 2 {
		return compressionResult{}, false
	}

	var persisted []providers.Message
	if ts := m.al.getActiveTurnState(sessionKey); ts != nil && ts.agentID == agent.ID {
		persisted = ts.persistedMessagesSnapshot()
	}
	stable, activeTurn := splitHistoryForActiveTurn(history, persisted)
	if len(stable) == 0 {
		return compressionResult{}, false
	}

	turns := parseTurnBoundaries(stable)
	var mid int
	if len(turns) >= 2 {
		mid = turns[len(turns)/2]
	} else {
		mid = findSafeBoundary(stable, len(stable)/2)
	}
	var keptHistory []providers.Message
	switch {
	case mid > 0:
		keptHistory = append([]providers.Message(nil), stable[mid:]...)
	case len(activeTurn) == 0:
		for i := len(stable) - 1; i >= 0; i-- {
			if stable[i].Role == "user" {
				keptHistory = []providers.Message{stable[i]}
				break
			}
		}
	}
	keptHistory = append(keptHistory, activeTurn...)

	droppedCount := len(history) - len(keptHistory)
	if droppedCount <= 0 {
		return compressionResult{}, false
	}

	existingSummary := agent.Sessions.GetSummary(sessionKey)
	compressionNote := fmt.Sprintf(
		"[Emergency compression dropped %d oldest messages due to context limit]",
		droppedCount,
	)
	if existingSummary != "" {
		compressionNote = existingSummary + "\n\n" + compressionNote
	}
	agent.Sessions.SetSummary(sessionKey, compressionNote)

	agent.Sessions.SetHistory(sessionKey, keptHistory)
	agent.Sessions.Save(sessionKey)

	logger.WarnCF("agent", "Forced compression executed", map[string]any{
		"session_key":  sessionKey,
		"dropped_msgs": droppedCount,
		"new_count":    len(keptHistory),
	})

	return compressionResult{
		DroppedMessages:   droppedCount,
		RemainingMessages: len(keptHistory),
	}, true
}

func (m *defaultContextManager) summarizeSession(agent *AgentInstance, sessionKey string) {
	if _, _, err := agent.primaryModel(); err != nil {
		// Summaries run on the agent's model; without one the history stays
		// as it is, still bounded by emergency compression.
		logger.InfoCF("agent", "Summarization skipped: the agent has no model", map[string]any{
			"agent_id":    agent.ID,
			"session_key": sessionKey,
			"reason":      err.Error(),
		})
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	history := agent.Sessions.GetHistory(sessionKey)
	summary := agent.Sessions.GetSummary(sessionKey)

	if len(history) <= 4 {
		return
	}

	safeCut := findSafeBoundary(history, len(history)-4)
	if safeCut <= 0 {
		return
	}
	keepCount := len(history) - safeCut
	toSummarize := history[:safeCut]

	maxMessageTokens := agent.ContextWindow / 2
	validMessages := make([]providers.Message, 0)
	omitted := false

	for _, msg := range toSummarize {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		msgTokens := len(msg.Content) / 2
		if msgTokens > maxMessageTokens {
			omitted = true
			continue
		}
		validMessages = append(validMessages, msg)
	}

	if len(validMessages) == 0 {
		return
	}

	const (
		maxSummarizationMessages = 10
		llmMaxRetries            = 3
	)

	// The new summary replaces the existing one, so every path folds the
	// existing summary in. When the model fails, nothing is committed: the
	// history stays whole (bounded by emergency compression) rather than being
	// truncated behind an excerpt of itself.
	var finalSummary string
	mid := 0
	if len(validMessages) > maxSummarizationMessages {
		mid = m.findNearestUserMessage(validMessages, len(validMessages)/2)
	}
	if mid > 0 && mid < len(validMessages) {
		s1, err := m.summarizeBatch(ctx, agent, validMessages[:mid], summary)
		if err != nil {
			logSummarizationFailure(agent, sessionKey, err)
			return
		}
		s2, err := m.summarizeBatch(ctx, agent, validMessages[mid:], "")
		if err != nil {
			logSummarizationFailure(agent, sessionKey, err)
			return
		}

		var merge strings.Builder
		merge.WriteString("Merge these conversation summaries, oldest first, into one cohesive summary. " +
			"Keep every fact the later ones do not replace.\n")
		if summary != "" {
			fmt.Fprintf(&merge, "\nEarlier summary: %s\n", summary)
		}
		fmt.Fprintf(&merge, "\n1: %s\n\n2: %s", s1, s2)
		resp, err := m.retryLLMCall(ctx, agent, merge.String(), llmMaxRetries)
		if err == nil && resp != nil && strings.TrimSpace(resp.Content) != "" {
			finalSummary = strings.TrimSpace(resp.Content)
		} else {
			// Both parts are the model's summaries, and s1 already carries
			// the earlier summary forward.
			finalSummary = s1 + "\n\n" + s2
		}
	} else {
		var err error
		finalSummary, err = m.summarizeBatch(ctx, agent, validMessages, summary)
		if err != nil {
			logSummarizationFailure(agent, sessionKey, err)
			return
		}
	}

	if omitted && finalSummary != "" {
		finalSummary += "\n[Note: Some oversized messages were omitted from this summary for efficiency.]"
	}

	if finalSummary == "" {
		return
	}
	// Messages appended while the model wrote the summary are kept: the
	// commit drops exactly the summarized messages, and nothing when they are
	// no longer the oldest of the session (it was cleared or compacted).
	if !session.CommitSummary(agent.Sessions, sessionKey, toSummarize, finalSummary) {
		logger.InfoCF("agent", "Summary discarded: the session changed while it was written", map[string]any{
			"agent_id":    agent.ID,
			"session_key": sessionKey,
		})
		return
	}
	agent.Sessions.Save(sessionKey)
	m.al.emitEvent(
		runtimeevents.KindAgentSessionSummarize,
		m.al.newTurnEventScope(agent.ID, sessionKey, nil).meta(0, "summarizeSession", "turn.session.summarize"),
		SessionSummarizePayload{
			SummarizedMessages: len(validMessages),
			KeptMessages:       keepCount,
			SummaryLen:         len(finalSummary),
			OmittedOversized:   omitted,
		},
	)
}

func logSummarizationFailure(agent *AgentInstance, sessionKey string, err error) {
	logger.WarnCF("agent", "Summarization failed; history kept as it is", map[string]any{
		"agent_id":    agent.ID,
		"session_key": sessionKey,
		"error":       err.Error(),
	})
}

func (m *defaultContextManager) findNearestUserMessage(messages []providers.Message, mid int) int {
	originalMid := mid

	for mid > 0 && messages[mid].Role != "user" {
		mid--
	}

	if messages[mid].Role == "user" {
		return mid
	}

	mid = originalMid
	for mid < len(messages) && messages[mid].Role != "user" {
		mid++
	}

	if mid < len(messages) {
		return mid
	}

	return originalMid
}

func (m *defaultContextManager) retryLLMCall(
	ctx context.Context,
	agent *AgentInstance,
	prompt string,
	maxRetries int,
) (*providers.LLMResponse, error) {
	const llmTemperature = 0.3

	provider, model, err := agent.primaryModel()
	if err != nil {
		logger.InfoCF("agent", "Summarization skipped: the agent has no model", map[string]any{
			"agent_id": agent.ID,
			"reason":   err.Error(),
		})
		return nil, err
	}

	var resp *providers.LLMResponse
	for attempt := 0; attempt < maxRetries; attempt++ {
		m.al.activeRequestsInc()
		resp, err = func() (*providers.LLMResponse, error) {
			defer m.al.activeRequestsDec()
			return provider.Chat(
				ctx,
				[]providers.Message{{Role: "user", Content: prompt}},
				nil,
				model,
				map[string]any{
					"max_tokens":       agent.MaxTokens,
					"temperature":      llmTemperature,
					"prompt_cache_key": agent.ID,
				},
			)
		}()

		if err == nil && resp != nil && resp.Content != "" {
			return resp, nil
		}
		if attempt < maxRetries-1 {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}

	return resp, err
}

// summarizeBatch asks the model for a summary of batch that folds in
// existingSummary. It fails when the model gives none: an excerpt of the
// messages is no summary, and committing one would truncate the history
// behind it.
func (m *defaultContextManager) summarizeBatch(
	ctx context.Context,
	agent *AgentInstance,
	batch []providers.Message,
	existingSummary string,
) (string, error) {
	const llmMaxRetries = 3

	var sb strings.Builder
	sb.WriteString("Provide a concise summary of this conversation segment, preserving core context and key points.\n")
	if existingSummary != "" {
		sb.WriteString("Existing context (fold it into the summary): ")
		sb.WriteString(existingSummary)
		sb.WriteString("\n")
	}
	sb.WriteString("\nCONVERSATION:\n")
	for _, msg := range batch {
		fmt.Fprintf(&sb, "%s: %s\n", msg.Role, msg.Content)
	}
	prompt := sb.String()

	response, err := m.retryLLMCall(ctx, agent, prompt, llmMaxRetries)
	if err != nil {
		return "", err
	}
	if response == nil || strings.TrimSpace(response.Content) == "" {
		return "", fmt.Errorf("the model returned an empty summary")
	}
	return strings.TrimSpace(response.Content), nil
}

func (m *defaultContextManager) estimateTokens(messages []providers.Message) int {
	total := 0
	for _, msg := range messages {
		total += EstimateMessageTokens(msg)
	}
	return total
}
