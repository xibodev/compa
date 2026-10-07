package commands

import (
	"context"
	"strings"
	"testing"
)

func TestApproveAndDenyAreListedInHelp(t *testing.T) {
	reply := formatHelpMessage(BuiltinDefinitions())
	for _, usage := range []string{"/approve <id>", "/deny <id>"} {
		if !strings.Contains(reply, usage) {
			t.Fatalf("/help reply missing %s, got %q", usage, reply)
		}
	}
}

// The agent answers /approve and /deny before commands run; the executor
// must pass them through rather than handle them a second time.
func TestApproveAndDenyPassThroughTheExecutor(t *testing.T) {
	ex := NewExecutor(NewRegistry(BuiltinDefinitions()), &Runtime{})
	for _, text := range []string{"/approve abc234", "/deny abc234", "/approve"} {
		replied := false
		res := ex.Execute(context.Background(), Request{
			Text: text,
			Reply: func(string) error {
				replied = true
				return nil
			},
		})
		if res.Outcome != OutcomePassthrough || replied {
			t.Fatalf("%q: outcome=%v replied=%v, want a silent passthrough", text, res.Outcome, replied)
		}
	}
}
