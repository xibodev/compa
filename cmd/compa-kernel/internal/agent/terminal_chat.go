package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/xibodev/compa/v4/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/commands"
)

// cliChannel is the channel the terminal's turns run on.
const cliChannel = "cli"

// terminalChat runs the interactive chat's turns one at a time, in the order
// they were typed. A turn can post a request in the chat -- the owner's
// approval of a tool call -- and wait for the answer, so once one is posted the
// chat keeps reading lines until its turns are done: an /approve or /deny goes
// to the agent at once, and any other line waits its turn.
type terminalChat struct {
	process func(input string) (string, error)
	out     io.Writer
	turns   chan string

	mu sync.Mutex
	// queued counts the turns submitted and not finished.
	queued int
	// asking is set when a turn posts in the chat, until the turns are done.
	asking bool
	// changed is signaled when a turn ends or posts in the chat.
	changed chan struct{}
}

func newTerminalChat(process func(input string) (string, error), out io.Writer) *terminalChat {
	c := &terminalChat{
		process: process,
		out:     out,
		turns:   make(chan string, 64),
		changed: make(chan struct{}, 1),
	}
	go c.work()
	return c
}

// handle runs input, returning once the next line can be read: when the turns
// are done, or when one posted in the chat, such as an approval request.
func (c *terminalChat) handle(input string) {
	if isApprovalAnswer(input) {
		c.run(input)
		return
	}
	c.mu.Lock()
	c.queued++
	c.mu.Unlock()
	c.turns <- input
	for {
		c.mu.Lock()
		ready := c.queued == 0 || c.asking
		c.mu.Unlock()
		if ready {
			return
		}
		<-c.changed
	}
}

func (c *terminalChat) work() {
	for input := range c.turns {
		c.run(input)
		c.mu.Lock()
		c.queued--
		if c.queued == 0 {
			c.asking = false
		}
		c.mu.Unlock()
		c.signal()
	}
}

func (c *terminalChat) run(input string) {
	response, err := c.process(input)
	if err != nil {
		fmt.Fprintf(c.out, "Error: %v\n", err)
		return
	}
	fmt.Fprintf(c.out, "\n%s %s\n\n", internal.Logo, response)
}

func (c *terminalChat) signal() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// printPosted prints, until out closes or ctx ends, the messages the agent
// posts in the terminal's chat outside a turn's reply, such as an approval
// request. Progress messages (tool feedback, thoughts) are left out, as the
// terminal never showed them; nothing here delivers other chats' messages.
func (c *terminalChat) printPosted(ctx context.Context, out <-chan bus.OutboundMessage) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-out:
			if !ok {
				return
			}
			if !isTerminalChatMessage(msg) {
				continue
			}
			fmt.Fprintf(c.out, "\n%s %s\n\n", internal.Logo, msg.Content)
			c.mu.Lock()
			if c.queued > 0 {
				c.asking = true
			}
			c.mu.Unlock()
			c.signal()
		}
	}
}

// isTerminalChatMessage reports whether msg is a chat message for the
// terminal. Channels mark progress messages with a "message_kind".
func isTerminalChatMessage(msg bus.OutboundMessage) bool {
	channel := msg.Context.Channel
	if channel == "" {
		channel = msg.Channel
	}
	if channel != cliChannel || strings.TrimSpace(msg.Content) == "" {
		return false
	}
	return strings.TrimSpace(msg.Context.Raw["message_kind"]) == ""
}

// isApprovalAnswer reports whether input is an /approve or /deny.
func isApprovalAnswer(input string) bool {
	name, ok := commands.CommandName(input)
	return ok && (name == "approve" || name == "deny")
}
