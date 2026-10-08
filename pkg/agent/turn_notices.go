package agent

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/logger"
)

// turnNoticeWait bounds how long a turn waits for the channel to have its
// start notice before it goes on anyway.
const turnNoticeWait = 5 * time.Second

// turnNotices tells a chat's channel when the turn answering its messages
// starts and ends, for a channel that observes turns
// (channels.TurnObserver), as the web chat does. Notices travel on the bus
// with the chat's replies, so the channel gets them in order:
//   - the start notice goes out as the turn starts, and the turn waits until
//     the channel has it, so no frame of the reply comes before it;
//   - the end notice goes out after the turn's reply was published, so it
//     comes after the reply.
//
// The notices name the messages the turn answers: those it started with,
// then those that joined it while it ran.
type turnNotices struct {
	al      *AgentLoop
	channel string
	chatID  string
	inbound *bus.InboundContext

	mu      sync.Mutex
	claimed bool
	ended   bool
	ids     []string
	status  TurnEndStatus
	failure string
}

type turnNoticesKey struct{}

// newTurnNotices returns the notices for the next turn answering chatID on
// channel, or nil when the channel doesn't observe turns.
func (al *AgentLoop) newTurnNotices(channel, chatID string, inbound *bus.InboundContext) *turnNotices {
	cm := al.currentChannelManager()
	if cm == nil || channel == "" || chatID == "" {
		return nil
	}
	ch, ok := cm.GetChannel(channel)
	if !ok {
		return nil
	}
	if _, observes := ch.(channels.TurnObserver); !observes {
		return nil
	}
	return &turnNotices{al: al, channel: channel, chatID: chatID, inbound: cloneInboundContext(inbound)}
}

// withTurnNotices makes n the notices of the first top-level turn run with
// the returned context.
func withTurnNotices(ctx context.Context, n *turnNotices) context.Context {
	if n == nil {
		return ctx
	}
	return context.WithValue(ctx, turnNoticesKey{}, n)
}

func turnNoticesFrom(ctx context.Context) *turnNotices {
	n, _ := ctx.Value(turnNoticesKey{}).(*turnNotices)
	return n
}

// claim binds the notices to the top-level turn ts, if no turn has them
// yet, and records the messages it starts with: the steering a continuation
// takes up, or else the message that started it.
func (n *turnNotices) claim(ts *turnState) bool {
	if n == nil || ts == nil || ts.depth != 0 {
		return false
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.claimed {
		return false
	}
	n.claimed = true
	if len(ts.opts.InitialSteeringMessages) > 0 {
		for _, msg := range ts.opts.InitialSteeringMessages {
			n.addLocked(msg.MessageID)
		}
	} else if inbound := ts.opts.Dispatch.InboundContext; inbound != nil {
		n.addLocked(inbound.MessageID)
	}
	return true
}

// start sends the start notice and waits until the channel has it.
func (n *turnNotices) start(ctx context.Context) {
	n.mu.Lock()
	notice := bus.TurnNotice{
		MessageIDs: slices.Clone(n.ids),
		Delivered:  make(chan struct{}),
	}
	n.mu.Unlock()
	if !n.publish(ctx, notice) {
		return
	}
	select {
	case <-notice.Delivered:
	case <-ctx.Done():
	case <-time.After(turnNoticeWait):
		logger.WarnCF("agent", "The channel didn't take the turn's start notice in time; going on", map[string]any{
			"channel": n.channel,
			"chat_id": n.chatID,
		})
	}
}

// answer records that the message with id joined the turn.
func (n *turnNotices) answer(id string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.addLocked(id)
}

func (n *turnNotices) addLocked(id string) {
	if id != "" && !slices.Contains(n.ids, id) {
		n.ids = append(n.ids, id)
	}
}

// finish records how the turn ended.
func (n *turnNotices) finish(status TurnEndStatus, failure string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.status = status
	n.failure = failure
}

// end sends the end notice, once, if a turn started. Call it after the
// turn's reply was published.
func (n *turnNotices) end(ctx context.Context) {
	if n == nil {
		return
	}
	n.mu.Lock()
	if !n.claimed || n.ended {
		n.mu.Unlock()
		return
	}
	n.ended = true
	status := n.status
	if status == "" {
		status = TurnEndStatusCompleted
	}
	notice := bus.TurnNotice{
		Ended:      true,
		MessageIDs: slices.Clone(n.ids),
		Status:     string(status),
	}
	if status == TurnEndStatusError && n.failure != "" {
		notice.Error = n.failure
		if cfg := n.al.GetConfig(); cfg != nil {
			notice.Error = cfg.FilterSensitiveData(n.failure)
		}
	}
	n.mu.Unlock()
	// The end notice goes out even when the turn's context was canceled, so
	// the channel's clients don't wait for it.
	n.publish(context.WithoutCancel(ctx), notice)
}

func (n *turnNotices) publish(ctx context.Context, notice bus.TurnNotice) bool {
	msg := bus.OutboundMessage{
		Channel: n.channel,
		ChatID:  n.chatID,
		Context: outboundContextFromInbound(n.inbound, n.channel, n.chatID, ""),
		Turn:    &notice,
	}
	pubCtx, cancel := context.WithTimeout(ctx, outboundPublishTimeout)
	defer cancel()
	if err := n.al.bus.PublishOutbound(pubCtx, msg); err != nil {
		logger.WarnCF("agent", "Failed to publish a turn notice", map[string]any{
			"channel": n.channel,
			"chat_id": n.chatID,
			"ended":   notice.Ended,
			"error":   err.Error(),
		})
		return false
	}
	return true
}
