//go:build paused_channels

package irc

import (
	"context"
	"testing"
	"time"

	"github.com/ergochat/irc-go/ircevent"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/pairing"
)

func newAccessTestChannel(t *testing.T, allowFrom ...string) (*IRCChannel, *bus.MessageBus) {
	t.Helper()
	msgBus := bus.NewMessageBus()
	bc := &config.Channel{Type: config.ChannelIRC, Enabled: true, AllowFrom: allowFrom}
	ch, err := NewIRCChannel(bc, &config.IRCSettings{Server: "irc.example.com:6697", Nick: "compa"}, msgBus)
	if err != nil {
		t.Fatalf("NewIRCChannel: %v", err)
	}
	ch.ctx = context.Background()
	ch.SetAccessPolicy(config.DMPolicyPairing, config.GroupPolicyAllowlist)
	return ch, msgBus
}

func TestOnPrivmsgRecordsUnpairedSender(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	ch, msgBus := newAccessTestChannel(t, "alice")

	ch.onPrivmsg(&ircevent.Connection{Nick: "compa"}, privmsg(t, ":Eve!e@host PRIVMSG compa :hi"))

	select {
	case inbound := <-msgBus.InboundChan():
		t.Fatalf("an unpaired sender's message was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
	if _, ok := ch.dmNicks.Load("eve"); ok {
		t.Error("an unpaired sender's nick was kept for replies")
	}
	requests, err := pairing.List(home, "irc")
	if err != nil || len(requests) != 1 || requests[0].SenderID != "irc:eve" {
		t.Fatalf("pairing requests = %+v, %v; want the stranger", requests, err)
	}
}

func TestOnPrivmsgAdmitsMembersOfAllowListedChannels(t *testing.T) {
	ch, msgBus := newAccessTestChannel(t, "alice", "#compa")

	ch.onPrivmsg(&ircevent.Connection{Nick: "compa"}, privmsg(t, ":bob!b@host PRIVMSG #compa :status?"))

	select {
	case inbound := <-msgBus.InboundChan():
		if inbound.Content != "status?" || inbound.Context.ChatType != "group" || inbound.Context.SenderIsOwner {
			t.Fatalf("content %q, chat type %q, owner %v",
				inbound.Content, inbound.Context.ChatType, inbound.Context.SenderIsOwner)
		}
	case <-time.After(time.Second):
		t.Fatal("a member of an allow-listed channel was not admitted")
	}

	// Other channels stay closed to senders not listed.
	ch.onPrivmsg(&ircevent.Connection{Nick: "compa"}, privmsg(t, ":bob!b@host PRIVMSG #other :status?"))
	select {
	case inbound := <-msgBus.InboundChan():
		t.Fatalf("a message from a channel not listed was published: %#v", inbound)
	case <-time.After(50 * time.Millisecond):
	}
}
