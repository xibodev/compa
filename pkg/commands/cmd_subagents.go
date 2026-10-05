package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const noActiveTasksMsg = "No active tasks running in this session."

// TurnInfo is a mirrored struct from agent.ActiveTurnInfo to avoid circular
// dependencies: its fields carry the same names.
type TurnInfo struct {
	TurnID       string
	ParentTurnID string
	Depth        int
	ChildTurnIDs []string
	IsFinished   bool
	// Phase and Iteration tell how far the turn got: "tools" in its second
	// iteration, say.
	Phase     string
	Iteration int
}

func subagentsCommand() Definition {
	return Definition{
		Name:        "subagents",
		Description: "Show running subagents and task tree",
		Handler: func(ctx context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.GetActiveTurn == nil {
				return req.Reply(unavailableMsg)
			}

			turnRaw := rt.GetActiveTurn()
			if treeStr, ok := turnRaw.(string); ok {
				if treeStr == "" {
					return req.Reply(noActiveTasksMsg)
				}
				return req.Reply(fmt.Sprintf("🤖 **Active Subagents Tree**\n```text\n%s\n```", treeStr))
			}

			info, ok := turnInfoOf(turnRaw)
			if !ok {
				return req.Reply("The active task can't be shown.")
			}
			if info.TurnID == "" {
				return req.Reply(noActiveTasksMsg)
			}
			return req.Reply(formatTurnInfo(info))
		},
	}
}

// turnInfoOf reads what Runtime.GetActiveTurn returned: nothing, a TurnInfo,
// or the agent's own turn struct, whose exported fields TurnInfo mirrors by
// name. ok is false for a value it can't read.
func turnInfoOf(v any) (info TurnInfo, ok bool) {
	switch t := v.(type) {
	case nil:
		return TurnInfo{}, true
	case TurnInfo:
		return t, true
	case *TurnInfo:
		if t != nil {
			info = *t
		}
		return info, true
	}
	data, err := json.Marshal(v)
	if err != nil {
		return TurnInfo{}, false
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return TurnInfo{}, false
	}
	return info, true
}

func formatTurnInfo(info TurnInfo) string {
	var b strings.Builder
	b.WriteString("🤖 **Active Task**\n")
	fmt.Fprintf(&b, "Turn: %s", info.TurnID)
	if info.Phase != "" {
		fmt.Fprintf(&b, " (%s", info.Phase)
		if info.Iteration > 0 {
			fmt.Fprintf(&b, ", iteration %d", info.Iteration)
		}
		b.WriteString(")")
	}
	if info.ParentTurnID != "" {
		fmt.Fprintf(&b, "\nParent: %s (depth %d)", info.ParentTurnID, info.Depth)
	}
	if len(info.ChildTurnIDs) == 0 {
		b.WriteString("\nNo subagents started.")
		return b.String()
	}
	fmt.Fprintf(&b, "\nSubagents (%d):", len(info.ChildTurnIDs))
	for _, id := range info.ChildTurnIDs {
		fmt.Fprintf(&b, "\n- %s", id)
	}
	return b.String()
}
