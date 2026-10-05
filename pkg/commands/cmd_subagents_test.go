package commands

import (
	"context"
	"strings"
	"testing"
	"time"
)

// agentTurnInfo has the shape of agent.ActiveTurnInfo, which this package
// can't import.
type agentTurnPhase string

type agentTurnInfo struct {
	TurnID       string
	AgentID      string
	UserMessage  string
	Phase        agentTurnPhase
	Iteration    int
	StartedAt    time.Time
	Depth        int
	ParentTurnID string
	ChildTurnIDs []string
}

func runSubagents(t *testing.T, rt *Runtime) string {
	t.Helper()
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), rt)
	var reply string
	res := ex.Execute(context.Background(), Request{
		Text: "/subagents",
		Reply: func(text string) error {
			reply = text
			return nil
		},
	})
	if res.Outcome != OutcomeHandled {
		t.Fatalf("outcome=%v, want=%v", res.Outcome, OutcomeHandled)
	}
	return reply
}

func TestSubagents_NilRuntime(t *testing.T) {
	for _, rt := range []*Runtime{nil, {}} {
		if reply := runSubagents(t, rt); reply != unavailableMsg {
			t.Fatalf("reply=%q, want %q", reply, unavailableMsg)
		}
	}
}

func TestSubagents_NoActiveTurn(t *testing.T) {
	var none *agentTurnInfo
	for _, turn := range []any{nil, none, ""} {
		reply := runSubagents(t, &Runtime{GetActiveTurn: func() any { return turn }})
		if reply != noActiveTasksMsg {
			t.Fatalf("turn %#v: reply=%q, want %q", turn, reply, noActiveTasksMsg)
		}
	}
}

func TestSubagents_FormatsTheAgentTurnReadably(t *testing.T) {
	turn := &agentTurnInfo{
		TurnID:       "turn-1",
		UserMessage:  "summarize the logs",
		Phase:        "tools",
		Iteration:    2,
		StartedAt:    time.Now(),
		ChildTurnIDs: []string{"turn-1-a", "turn-1-b"},
	}
	reply := runSubagents(t, &Runtime{GetActiveTurn: func() any { return turn }})

	want := "🤖 **Active Task**\nTurn: turn-1 (tools, iteration 2)\nSubagents (2):\n- turn-1-a\n- turn-1-b"
	if reply != want {
		t.Fatalf("reply=%q, want %q", reply, want)
	}
	if strings.Contains(reply, "{") || strings.Contains(reply, "summarize the logs") {
		t.Fatalf("reply=%q prints the raw struct", reply)
	}
}

func TestSubagents_ChildTurnWithoutSubagents(t *testing.T) {
	turn := TurnInfo{TurnID: "turn-2", ParentTurnID: "turn-1", Depth: 1, Phase: "running", Iteration: 1}
	reply := runSubagents(t, &Runtime{GetActiveTurn: func() any { return turn }})

	want := "🤖 **Active Task**\nTurn: turn-2 (running, iteration 1)\nParent: turn-1 (depth 1)\nNo subagents started."
	if reply != want {
		t.Fatalf("reply=%q, want %q", reply, want)
	}
}
