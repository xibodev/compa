// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"

	"github.com/xibodev/compa/pkg/bus"
	runtimeevents "github.com/xibodev/compa/pkg/events"
	"github.com/xibodev/compa/pkg/providers"
)

// Finalize handles turn finalization, either:
// - Early return when allResponsesHandled=true (ExecuteTools already finalized)
// - Normal finalization for allResponsesHandled=false (sets finalContent, saves session, compact)
func (p *Pipeline) Finalize(
	ctx context.Context,
	turnCtx context.Context,
	ts *turnState,
	exec *turnExecution,
	turnStatus TurnEndStatus,
	finalContent string,
) (turnResult, error) {
	al := p.al

	// When allResponsesHandled=true, ExecuteTools already finalized
	// (added handledToolResponseSummary, saved session, set phase to Completed).
	// But still check for hard abort - if requested, abort the turn.
	if exec.allResponsesHandled {
		if ts.hardAbortRequested() {
			return al.abortTurn(ts)
		}
		ts.setPhase(TurnPhaseCompleted)
		return turnResult{
			finalContent:       finalContent,
			modelName:          exec.llmModelName,
			requestedSelection: exec.requestedSelection,
			servedTarget:       exec.servedTarget,
			servedIdentity:     exec.servedIdentity,
			status:             turnStatus,
			followUps:          append([]bus.InboundMessage(nil), ts.followUps...),
		}, nil
	}

	ts.setPhase(TurnPhaseFinalizing)
	ts.setFinalContent(finalContent)
	if !ts.opts.NoHistory {
		finalMsg := providers.Message{
			Role:               "assistant",
			Content:            finalContent,
			ModelName:          exec.llmModelName,
			RequestedSelection: exec.requestedSelection,
			ServedTarget:       exec.servedTarget,
			ServedIdentity:     exec.servedIdentity,
			ReasoningContent:   responseReasoningContent(exec.response),
		}
		ts.agent.Sessions.AddFullMessage(ts.sessionKey, finalMsg)
		ts.recordPersistedMessage(finalMsg)
		ts.ingestMessage(turnCtx, al, finalMsg)
		if err := ts.agent.Sessions.Save(ts.sessionKey); err != nil {
			al.emitEvent(
				runtimeevents.KindAgentError,
				ts.eventMeta("runTurn", "turn.error"),
				ErrorPayload{
					Stage:   "session_save",
					Message: err.Error(),
				},
			)
			cancelConfiguredStreamingLLM(turnCtx, exec)
			return turnResult{status: TurnEndStatusError}, err
		}
	}

	if !ts.opts.NoHistory && ts.opts.EnableSummary {
		al.contextManager.Compact(
			turnCtx,
			&CompactRequest{
				SessionKey: ts.sessionKey,
				Reason:     ContextCompressReasonSummarize,
				Budget:     ts.agent.ContextWindow,
			},
		)
	}

	contextUsage := computeContextUsage(ts.agent, ts.sessionKey)
	streamErr := finalizeConfiguredStreamingLLM(turnCtx, ts, exec, finalContent, contextUsage)
	// If streaming never became visible, publish the final answer directly so it
	// still reaches the web chat when the turn does not send its response (SendResponse
	// is false and AllowInterimWebPublish is set).
	if ((streamErr != nil && !isAfterOutputError(streamErr)) || exec.streamingFallback) &&
		!ts.opts.SendResponse && ts.opts.AllowInterimWebPublish && finalContent != "" {
		msg := outboundMessageForTurnWithOptions(ts, finalContent, outboundTurnMessageOptions{
			modelName: exec.llmModelName,
			raw:       instanceSelectionOutboundMetadata(exec),
		})
		msg.ContextUsage = contextUsage
		markFinalOutbound(&msg)
		_ = al.bus.PublishOutbound(turnCtx, msg)
	}
	if streamErr != nil && isAfterOutputError(streamErr) {
		ts.setPhase(TurnPhaseCompleted)
		return turnResult{
			finalContent:       finalContent,
			requestedSelection: exec.requestedSelection,
			servedTarget:       exec.servedTarget,
			servedIdentity:     exec.servedIdentity,
			status:             TurnEndStatusError,
			followUps:          append([]bus.InboundMessage(nil), ts.followUps...),
		}, streamErr
	}
	ts.setPhase(TurnPhaseCompleted)
	return turnResult{
		finalContent:       finalContent,
		modelName:          exec.llmModelName,
		requestedSelection: exec.requestedSelection,
		servedTarget:       exec.servedTarget,
		servedIdentity:     exec.servedIdentity,
		status:             turnStatus,
		followUps:          append([]bus.InboundMessage(nil), ts.followUps...),
	}, nil
}
