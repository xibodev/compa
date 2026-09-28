package agent

import runtimeevents "github.com/xibodev/compa/pkg/events"

func (al *AgentLoop) publishRuntimeEvent(evt runtimeevents.Event) {
	if al == nil || al.runtimeEvents == nil {
		return
	}

	al.runtimeEvents.PublishNonBlocking(evt)
}

func runtimeScopeFromHookMeta(meta HookMeta, eventCtx *TurnContext) runtimeevents.Scope {
	scope := runtimeevents.Scope{
		AgentID:    meta.AgentID,
		SessionKey: meta.SessionKey,
		TurnID:     meta.TurnID,
	}

	if eventCtx == nil || eventCtx.Inbound == nil {
		return scope
	}

	inbound := eventCtx.Inbound
	scope.Channel = inbound.Channel
	scope.Account = inbound.Account
	scope.ChatID = inbound.ChatID
	scope.TopicID = inbound.TopicID
	scope.SpaceID = inbound.SpaceID
	scope.SpaceType = inbound.SpaceType
	scope.ChatType = inbound.ChatType
	scope.SenderID = inbound.SenderID
	scope.MessageID = inbound.MessageID
	return scope
}

func runtimeCorrelationFromHookMeta(meta HookMeta) runtimeevents.Correlation {
	return runtimeevents.Correlation{
		TraceID:      meta.TracePath,
		ParentTurnID: meta.ParentTurnID,
	}
}

func runtimeSeverityForAgentEvent(kind runtimeevents.Kind, payload any) runtimeevents.Severity {
	switch kind {
	case runtimeevents.KindAgentError, runtimeevents.KindAgentSubTurnOrphan:
		return runtimeevents.SeverityError
	case runtimeevents.KindAgentLLMRetry,
		runtimeevents.KindAgentContextCompress,
		runtimeevents.KindAgentToolExecSkipped:
		return runtimeevents.SeverityWarn
	case runtimeevents.KindAgentTurnEnd:
		payload, ok := payload.(TurnEndPayload)
		if !ok {
			return runtimeevents.SeverityInfo
		}
		switch payload.Status {
		case TurnEndStatusError:
			return runtimeevents.SeverityError
		case TurnEndStatusAborted:
			return runtimeevents.SeverityWarn
		default:
			return runtimeevents.SeverityInfo
		}
	case runtimeevents.KindAgentToolExecEnd:
		payload, ok := payload.(ToolExecEndPayload)
		if ok && payload.IsError {
			return runtimeevents.SeverityWarn
		}
		return runtimeevents.SeverityInfo
	default:
		return runtimeevents.SeverityInfo
	}
}

func runtimeAttrsFromHookMeta(meta HookMeta) map[string]any {
	attrs := make(map[string]any, 2)
	if meta.Source != "" {
		attrs["agent_source"] = meta.Source
	}
	if meta.Iteration != 0 {
		attrs["iteration"] = meta.Iteration
	}
	if len(attrs) == 0 {
		return nil
	}
	return attrs
}

// hookMetaFromRuntimeEvent recovers the hook correlation fields carried by an
// agent runtime event, reversing runtimeScopeFromHookMeta,
// runtimeCorrelationFromHookMeta and runtimeAttrsFromHookMeta.
func hookMetaFromRuntimeEvent(evt runtimeevents.Event) HookMeta {
	meta := HookMeta{
		AgentID:      evt.Scope.AgentID,
		TurnID:       evt.Scope.TurnID,
		ParentTurnID: evt.Correlation.ParentTurnID,
		SessionKey:   evt.Scope.SessionKey,
		TracePath:    evt.Correlation.TraceID,
	}
	if evt.Attrs != nil {
		if source, ok := evt.Attrs["agent_source"].(string); ok {
			meta.Source = source
		}
		if iteration, ok := evt.Attrs["iteration"].(int); ok {
			meta.Iteration = iteration
		}
	}
	return meta
}
