// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/xibodev/compa/v4/pkg/approval"
	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/config"
	runtimeevents "github.com/xibodev/compa/v4/pkg/events"
	"github.com/xibodev/compa/v4/pkg/providers"
	"github.com/xibodev/compa/v4/pkg/tools"
)

// The approval policy (tools.approval) decides every tool call: it runs, is
// refused, waits for an approval, or the tool is not offered at all. An ask
// goes to the registered approvers (ToolApprover hooks) or, with none, to the
// owner in chat (askOwner).

const (
	approvalPolicyDeniedReason = "Denied by the approval policy."
	// toolHiddenFormat refuses a call of a tool the turn is not offered.
	toolHiddenFormat = "Tool %q is not available."
)

// turnOrigin is the origin of a turn's calls: cron for a scheduled turn (a
// cron job or the heartbeat), otherwise that of the turn's channel.
func turnOrigin(scheduled bool, channel string) approval.Origin {
	channel = strings.TrimSpace(channel)
	switch {
	case scheduled:
		return approval.OriginCron
	case channel == config.ChannelWeb:
		return approval.OriginWeb
	case channel == terminalChannel:
		return approval.OriginCLI
	default:
		return approval.OriginChat
	}
}

func (al *AgentLoop) approvalPolicy() approval.Policy {
	if cfg := al.GetConfig(); cfg != nil {
		return cfg.Tools.Approval
	}
	return approval.Policy{}
}

// toolCallVerdict is what the approval policy lets a call do.
type toolCallVerdict struct {
	allowed bool
	// approved reports that the operator approved the call, by a rule that
	// allows it or by answering an ask: the tool runs with
	// approval.WithApproved.
	approved bool
	// denyContent tells the model why a call may not run.
	denyContent string
}

// authorizeToolCall decides call callID of ts's turn by the approval policy,
// asking when the policy says to.
func (al *AgentLoop) authorizeToolCall(
	ctx context.Context,
	ts *turnState,
	callID string,
	toolName string,
	toolArgs map[string]any,
) toolCallVerdict {
	tool, ok := ts.agent.Tools.Get(toolName)
	if !ok {
		// Nothing by that name could run; refusing here also keeps a tool
		// registered meanwhile from running undecided.
		return toolCallVerdict{denyContent: fmt.Sprintf(toolHiddenFormat, toolName)}
	}
	policyTool, info := approval.Describe(toolName, tool)
	decision := al.approvalPolicy().Decide(policyTool, ts.origin)
	switch decision.Action {
	case approval.Allow:
		return toolCallVerdict{allowed: true, approved: decision.Approved()}
	case approval.Hide:
		return toolCallVerdict{denyContent: fmt.Sprintf(toolHiddenFormat, toolName)}
	case approval.Ask:
		if !askApplies(toolName, toolArgs) {
			return toolCallVerdict{allowed: true}
		}
		approved, reason := al.askApproval(ctx, callID, &ToolApprovalRequest{
			Meta:      ts.eventMeta("runTurn", "turn.tool.approve"),
			Context:   cloneTurnContext(ts.turnCtx),
			Tool:      toolName,
			Arguments: toolArgs,
			Origin:    ts.origin,
			Info:      info,
			Decision:  decision,
		})
		return toolCallVerdict{allowed: approved, approved: approved, denyContent: reason}
	default:
		return toolCallVerdict{denyContent: approvalPolicyDeniedReason}
	}
}

// askApplies reports whether an ask covers the call. exec's list, poll,
// read and kill only look at or stop a process that an allowed or approved
// run started, and are not asked about. write and send-keys are: input sent
// to a process, a shell for one, runs things in it.
func askApplies(toolName string, args map[string]any) bool {
	if toolName != "exec" {
		return true
	}
	action, _ := args["action"].(string)
	switch action {
	case "list", "poll", "read", "kill":
		return false
	default:
		return true
	}
}

// askApproval decides a call the policy says to ask about: every registered
// approver must approve it, or with none the owner does, in chat. reason
// tells the model why a call that was not approved may not run. The ask is
// published as an approval.requested event, and its answer as an
// approval.resolved one; callID is the call's ID in a turn, if any.
func (al *AgentLoop) askApproval(
	ctx context.Context,
	callID string,
	req *ToolApprovalRequest,
) (approved bool, reason string) {
	meta := req.Meta
	if meta.turnContext == nil {
		// A call made outside a turn: the events name the chat it reports to.
		meta.turnContext = req.Context
	}
	id := fmt.Sprintf("approval-%d", al.approvalSeq.Add(1))
	al.emitEvent(runtimeevents.KindApprovalRequested, meta, ApprovalRequestedPayload{
		ApprovalID: id,
		ActionID:   callID,
		Tool:       req.Tool,
		Parameters: cloneEventArguments(req.Arguments),
		Reason:     approvalAskReason(req.Decision),
		State:      "pending",
	})

	approved, reason = al.askApprovers(ctx, req)

	resolution := "rejected"
	switch {
	case approved:
		resolution = "approved"
	case ctx.Err() != nil:
		resolution = "cancelled" // the turn or job ended before the answer
	}
	al.emitEvent(runtimeevents.KindApprovalResolved, meta, ApprovalResolvedPayload{
		ApprovalID: id,
		Resolution: resolution,
		Reason:     reason,
	})
	return approved, reason
}

// approvalAskReason says which part of the approval policy asks.
func approvalAskReason(decision approval.Decision) string {
	if decision.Rule == nil {
		return "tools.approval.default asks about this call"
	}
	return fmt.Sprintf("tools.approval.rules[%d] asks about this call", decision.Index)
}

// askApprovers has the registered approvers decide req's call, or with none
// the owner.
func (al *AgentLoop) askApprovers(ctx context.Context, req *ToolApprovalRequest) (approved bool, reason string) {
	if decision, decided := al.hooks.ApproveTool(ctx, req); decided {
		if !decision.Approved {
			return false, hookDeniedToolContent("Tool execution denied by approval hook", decision.Reason)
		}
		return true, ""
	}
	var inbound *bus.InboundContext
	if req.Context != nil {
		inbound = req.Context.Inbound
	}
	// The owner may take ownerApprovalTimeout to answer, so the owner is
	// asked outside the hooks' approval timeout.
	return al.askOwnerApproval(ctx, inbound, req.Tool, req.Arguments, req.Job)
}

var _ approval.Gate = (*AgentLoop)(nil)

// Check is the agent loop's approval.Gate, for the calls made outside a turn
// such as a scheduled command: the approval policy decides them as calls
// from their origin, cron unless the call says otherwise. An ask goes to the
// registered approvers, or with none to the owner, in the owner's chat.
func (al *AgentLoop) Check(ctx context.Context, call approval.Call) error {
	origin := call.Origin
	if origin == "" {
		origin = approval.OriginCron
	}
	var tool tools.Tool
	if agent := al.GetRegistry().GetDefaultAgent(); agent != nil && agent.Tools != nil {
		tool, _ = agent.Tools.Get(call.Tool)
	}
	policyTool, info := approval.Describe(call.Tool, tool)
	decision := al.approvalPolicy().Decide(policyTool, origin)
	switch decision.Action {
	case approval.Allow:
		return nil
	case approval.Hide:
		return fmt.Errorf(toolHiddenFormat, call.Tool)
	case approval.Ask:
		if !askApplies(call.Tool, call.Arguments) {
			return nil
		}
	default:
		return errors.New(approvalPolicyDeniedReason)
	}

	// Approval hooks load with the first turn; a job may run before it.
	_ = al.ensureHooksInitialized(ctx)
	// Nobody wrote a call made outside a turn: the owner is asked in the
	// owner's chat, whatever chat the call reports to.
	approved, reason := al.askApproval(withScheduledTurn(ctx), "", &ToolApprovalRequest{
		Context:   &TurnContext{Inbound: &bus.InboundContext{Channel: call.Channel, ChatID: call.ChatID}},
		Tool:      call.Tool,
		Arguments: call.Arguments,
		Origin:    origin,
		Info:      info,
		Decision:  decision,
		Job:       call.Job,
	})
	if !approved {
		return errors.New(reason)
	}
	return nil
}

// hiddenTools returns the test of the tools the approval policy hides from
// ts's turn; nil when it hides none.
func (al *AgentLoop) hiddenTools(ts *turnState) func(name string, tool tools.Tool) bool {
	policy := al.approvalPolicy()
	if policy.Default != approval.Hide && !slices.ContainsFunc(policy.Rules, func(r approval.Rule) bool {
		return r.Action == approval.Hide
	}) {
		return nil
	}
	origin := ts.origin
	return func(name string, tool tools.Tool) bool {
		policyTool, _ := approval.Describe(name, tool)
		return policy.Decide(policyTool, origin).Action == approval.Hide
	}
}

// offeredToolDefs returns the definitions of the tools ts's turn offers the
// model: its session's tools that the turn profile allows and the approval
// policy does not hide.
func (al *AgentLoop) offeredToolDefs(ts *turnState) []providers.ToolDefinition {
	defs := filterToolsByTurnProfile(ts.agent.Tools.ToProviderDefsForSession(ts.sessionKey), ts.profile)
	hidden := al.hiddenTools(ts)
	if hidden == nil {
		return defs
	}
	offered := make([]providers.ToolDefinition, 0, len(defs))
	for _, def := range defs {
		tool, _ := ts.agent.Tools.Get(def.Function.Name)
		if !hidden(def.Function.Name, tool) {
			offered = append(offered, def)
		}
	}
	return offered
}

// toolAllowed reports whether the approval policy lets ts's turn use a tool
// without asking, as native web search, which runs inside the provider, needs
// for web_search.
func (al *AgentLoop) toolAllowed(ts *turnState, name string) bool {
	tool, _ := ts.agent.Tools.Get(name)
	policyTool, _ := approval.Describe(name, tool)
	return al.approvalPolicy().Decide(policyTool, ts.origin).Action == approval.Allow
}
