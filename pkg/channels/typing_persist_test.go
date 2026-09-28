package channels

import (
	"testing"

	"github.com/xibodev/compa/pkg/bus"
)

// The bug this exists for: the typing indicator stopped on the first outbound
// message of a turn, which for a module turn is the TOOL CALL announcement --
// not the answer. The user then watched a dead screen while the real work ran.
//
// Measured through the real websocket: typing.stop at 1.9s, final answer at
// 11.3s, so 9.4 seconds with nothing on screen saying anything was happening.
// A module render takes far longer than that.
func TestAuxiliaryMessagesKeepTypingAlive(t *testing.T) {
	aux := []string{"tool_calls", "thought", "tool_feedback"}
	for _, kind := range aux {
		msg := bus.OutboundMessage{}
		msg.Context.Raw = map[string]string{"message_kind": kind}
		if !outboundMessageHasAuxiliaryKind(msg) {
			t.Errorf("%q is not recognised as auxiliary, so it will stop typing"+
				" and blank the indicator mid-turn", kind)
		}
	}
}

// A message that ends the turn must still stop the indicator, or it runs
// forever from the user's point of view.
func TestFinalMessageStopsTyping(t *testing.T) {
	final := bus.OutboundMessage{}
	final.Context.Raw = map[string]string{"outbound_kind": "final"}
	if outboundMessageHasAuxiliaryKind(final) {
		t.Fatal("a final message was classed auxiliary: typing would never stop")
	}

	// A plain message with no kind at all is the ordinary answer.
	plain := bus.OutboundMessage{}
	if outboundMessageHasAuxiliaryKind(plain) {
		t.Fatal("a plain message was classed auxiliary: typing would never stop")
	}
}
