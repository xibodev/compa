package channels

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
)

func TestInboundRateLimitPerSender(t *testing.T) {
	stubPairing(t)
	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("telegram", nil, msgBus, []string{"telegram:111"})
	ch.SetAccessPolicy(config.DMPolicyOpen, config.GroupPolicyOpen)

	stranger := telegramSender("999", "")
	for i := range senderRateBurst {
		if _, ok := deliver(t, ch, msgBus, "direct", "999", stranger); !ok {
			t.Fatalf("message %d within the burst was dropped", i+1)
		}
	}
	if _, ok := deliver(t, ch, msgBus, "group", "-100", stranger); ok {
		t.Fatal("a sender over the rate limit was not limited")
	}
	if _, ok := deliver(t, ch, msgBus, "direct", "888", telegramSender("888", "")); !ok {
		t.Fatal("one sender's limit applied to another sender")
	}
	owner := telegramSender("111", "")
	for i := range senderRateBurst + 5 {
		if _, ok := deliver(t, ch, msgBus, "direct", "111", owner); !ok {
			t.Fatalf("the owner's message %d was rate limited", i+1)
		}
	}
}

func TestSenderLimitsRefillAndPrune(t *testing.T) {
	var limits senderLimits
	now := time.Now()
	for range senderRateBurst {
		if !limits.allow("a", now) {
			t.Fatal("burst message refused")
		}
	}
	if limits.allow("a", now) {
		t.Fatal("message over the burst allowed")
	}
	if !limits.allow("a", now.Add(senderRateEvery)) {
		t.Fatal("the limit did not refill")
	}

	// Senders idle long enough to have refilled are forgotten first.
	later := now.Add(time.Hour)
	for i := range maxTrackedSenders - 1 {
		limits.allow(fmt.Sprintf("idle-%d", i), now)
	}
	limits.allow("fresh", later)
	if n := len(limits.limiters); n != 1 {
		t.Fatalf("%d limiters kept after pruning idle ones, want 1", n)
	}

	// Many active senders at once stay bounded.
	for i := range maxTrackedSenders + 10 {
		limits.allow(fmt.Sprintf("active-%d", i), later)
	}
	if n := len(limits.limiters); n > maxTrackedSenders {
		t.Fatalf("%d limiters kept, want at most %d", n, maxTrackedSenders)
	}
}

func TestInboundQueueCapDropsInsteadOfBlocking(t *testing.T) {
	stubPairing(t)
	old := maxQueuedInbound
	maxQueuedInbound = 1
	t.Cleanup(func() { maxQueuedInbound = old })

	msgBus := bus.NewMessageBus()
	ch := NewBaseChannel("telegram", nil, msgBus, nil)
	ch.SetAccessPolicy(config.DMPolicyOpen, config.GroupPolicyOpen)

	// Fill the bus so the next publish waits.
	ctx := context.Background()
	filled := 0
	for {
		pubCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
		err := msgBus.PublishInbound(pubCtx, bus.InboundMessage{Context: bus.InboundContext{
			Channel: "filler", ChatID: "c", SenderID: "s",
		}})
		cancel()
		if err != nil {
			break
		}
		filled++
	}

	blocked := make(chan error, 1)
	go func() {
		blocked <- ch.HandleMessageWithContext(ctx, "1", "waits", nil, bus.InboundContext{
			ChatID: "1", ChatType: "direct", SenderID: "1",
		}, telegramSender("1", ""))
	}()
	deadline := time.Now().Add(2 * time.Second)
	for ch.queuedInbound.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the first message never waited for the bus")
		}
		time.Sleep(5 * time.Millisecond)
	}

	done := make(chan error, 1)
	go func() {
		done <- ch.HandleMessageWithContext(ctx, "2", "dropped", nil, bus.InboundContext{
			ChatID: "2", ChatType: "direct", SenderID: "2",
		}, telegramSender("2", ""))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("HandleMessageWithContext() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a message over the queue cap blocked")
	}

	// Free one slot: the waiting message goes through, the dropped one not.
	<-msgBus.InboundChan()
	if err := <-blocked; err != nil {
		t.Fatalf("waiting message: %v", err)
	}
	contents := map[string]bool{}
	for range filled {
		msg := <-msgBus.InboundChan()
		contents[msg.Content] = true
	}
	if !contents["waits"] || contents["dropped"] {
		t.Fatalf("published contents = %v, want the waiting message only", contents)
	}
}
