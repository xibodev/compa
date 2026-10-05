package vk

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SevereCloud/vksdk/v3/api"
	"github.com/SevereCloud/vksdk/v3/api/params"
	"github.com/SevereCloud/vksdk/v3/events"
	"github.com/SevereCloud/vksdk/v3/longpoll-bot"
	"github.com/SevereCloud/vksdk/v3/object"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/channels"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/identity"
	"github.com/xibodev/compa/pkg/logger"
)

type VKChannel struct {
	*channels.BaseChannel
	vk          *api.VK
	lp          *longpoll.LongPoll
	channelName string
	bc          *config.Channel
	groupID     int            // the community the bot runs as
	mentionRe   *regexp.Regexp // a mention of groupID; see setGroupID
	ctx         context.Context
	cancel      context.CancelFunc
}

func NewVKChannel(channelName string, bc *config.Channel, bus *bus.MessageBus) (*VKChannel, error) {
	var vkCfg config.VKSettings
	if err := bc.Decode(&vkCfg); err != nil {
		return nil, err
	}

	vk := api.NewVK(vkCfg.Token.String())

	base := channels.NewBaseChannel(
		channelName,
		&vkCfg,
		bus,
		bc.AllowFrom,
		channels.WithMaxMessageLength(4000),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	return &VKChannel{
		BaseChannel: base,
		vk:          vk,
		channelName: channelName,
		bc:          bc,
	}, nil
}

func (c *VKChannel) getVKCfg() *config.VKSettings {
	var v config.VKSettings
	if err := c.bc.Decode(&v); err != nil {
		return nil
	}
	return &v
}

func (c *VKChannel) Start(ctx context.Context) error {
	logger.InfoC("vk", "Starting VK bot (Long Poll mode)...")

	c.ctx, c.cancel = context.WithCancel(ctx)

	groupID := c.getVKCfg().GroupID
	if groupID == 0 {
		c.cancel()
		return fmt.Errorf("group_id is required for VK bot")
	}
	c.setGroupID(groupID)

	lp, err := longpoll.NewLongPoll(c.vk, groupID)
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to create long poll: %w", err)
	}
	c.lp = lp

	lp.MessageNew(func(_ context.Context, obj events.MessageNewObject) {
		defer channels.RecoverPanic(c.Name(), "message")
		c.handleMessage(obj.Message)
	})

	c.SetRunning(true)

	logger.InfoCF("vk", "VK bot connected", map[string]any{
		"group_id": groupID,
	})

	go c.runLongPoll(c.ctx, lp)

	return nil
}

// Long-poll restart delays after a failure.
const (
	longPollRetryInitial = 2 * time.Second
	longPollRetryMax     = 2 * time.Minute
)

// runLongPoll runs the long poll until ctx ends, restarting it with a
// growing delay when it fails, for example when the network drops.
func (c *VKChannel) runLongPoll(ctx context.Context, lp *longpoll.LongPoll) {
	defer channels.RecoverPanic(c.Name(), "long poll")
	delay := longPollRetryInitial
	for {
		started := time.Now()
		err := lp.RunWithContext(ctx)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("long poll stopped")
		}
		if time.Since(started) > longPollRetryMax {
			delay = longPollRetryInitial
		}
		logger.ErrorCF("vk", "Long poll failed, restarting", map[string]any{
			"error": err.Error(),
			"delay": delay.String(),
		})
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, longPollRetryMax)
	}
}

func (c *VKChannel) Stop(ctx context.Context) error {
	logger.InfoC("vk", "Stopping VK bot...")
	c.SetRunning(false)

	if c.lp != nil {
		c.lp.Shutdown()
	}

	if c.cancel != nil {
		c.cancel()
	}

	return nil
}

func (c *VKChannel) handleMessage(msg object.MessagesMessage) {
	if msg.Action.Type != "" {
		return
	}

	if bool(msg.Out) {
		return
	}

	peerID := msg.PeerID
	chatID := strconv.Itoa(peerID)

	fromID := msg.FromID
	userID := strconv.Itoa(fromID)

	isGroupChat := peerID != fromID
	chatType := "direct"
	if isGroupChat {
		chatType = "group"
	}

	platformID := userID
	sender := bus.SenderInfo{
		Platform:    "vk",
		PlatformID:  platformID,
		CanonicalID: identity.BuildCanonicalID("vk", platformID),
	}

	text := msg.Text
	if text == "" && len(msg.Attachments) > 0 {
		text = c.processAttachments(msg.Attachments)
	}

	if text == "" {
		return
	}

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    chatID,
		ChatType:  chatType,
		SenderID:  userID,
		MessageID: strconv.Itoa(msg.ConversationMessageID),
		Raw: map[string]string{
			"user_id":  userID,
			"is_group": fmt.Sprintf("%t", isGroupChat),
		},
	}

	// Decide before looking the sender up in the VK API. A direct message
	// from an unpaired sender still goes to the access policy, which records
	// the sender.
	if !c.Admits(chatType, sender, chatID) {
		logger.DebugCF("vk", "Message from unauthorized user", map[string]any{
			"peer_id": peerID,
		})
		if !isGroupChat {
			c.HandleInboundContext(c.ctx, chatID, text, nil, inboundCtx, sender)
		}
		return
	}

	if isGroupChat {
		isMentioned := c.isMentioned(msg)
		if isMentioned {
			text = c.stripBotMention(text)
		}
		respond, cleaned := c.ShouldRespondInGroup(isMentioned, text)
		if !respond {
			return
		}
		text = cleaned
		inboundCtx.Mentioned = isMentioned
	}

	sender.DisplayName = c.getUserName(fromID)
	c.HandleInboundContext(c.ctx, chatID, text, nil, inboundCtx, sender)
}

func (c *VKChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	peerID, err := strconv.Atoi(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
	}

	if msg.Content == "" {
		return nil, nil
	}

	var messageIDs []string
	chunks := channels.SplitMessage(msg.Content, 4000)

	for _, chunk := range chunks {
		if chunk == "" {
			continue
		}

		b := params.NewMessagesSendBuilder()
		b.Message(chunk)
		b.RandomID(0)
		b.PeerID(peerID)

		if msg.ReplyToMessageID != "" {
			if replyID, err := strconv.Atoi(msg.ReplyToMessageID); err == nil {
				b.ReplyTo(replyID)
			}
		}

		resp, err := c.vk.MessagesSend(b.Params)
		if err != nil {
			logger.ErrorCF("vk", "Failed to send message", map[string]any{
				"error":   err.Error(),
				"peer_id": peerID,
			})
			return messageIDs, classifySendError(err)
		}

		messageIDs = append(messageIDs, strconv.Itoa(resp))
	}

	return messageIDs, nil
}

// classifySendError maps a VK error to the channel error kinds, so that the
// manager retries rate limits and server errors and gives up on the rest.
func classifySendError(err error) error {
	switch {
	case errors.Is(err, api.ErrTooMany), errors.Is(err, api.ErrFlood):
		return fmt.Errorf("failed to send message: %w: %v", channels.ErrRateLimit, err)
	case errors.Is(err, api.ErrServer), errors.Is(err, api.ErrUnknown):
		return fmt.Errorf("failed to send message: %w: %v", channels.ErrTemporary, err)
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("failed to send message: %w: %v", channels.ErrSendFailed, err)
	}
	return channels.ClassifyNetError(err)
}

// setGroupID records the community the bot runs as, which mentions name.
func (c *VKChannel) setGroupID(groupID int) {
	c.groupID = groupID
	id := strconv.Itoa(groupID)
	// VK writes a mention of a community as "[club123|Name]" ("public" for
	// public pages); a typed "@club123" may arrive unconverted.
	c.mentionRe = regexp.MustCompile(`\[(?:club|public)` + id + `\|[^\]]*\]|@(?:club|public)` + id + `\b`)
}

// isMentioned reports whether a chat message is addressed to the bot: it
// mentions the bot's community, or replies to one of the bot's messages
// (which come from the negated community ID).
func (c *VKChannel) isMentioned(msg object.MessagesMessage) bool {
	if c.groupID == 0 || c.mentionRe == nil {
		return false
	}
	if c.mentionRe.MatchString(msg.Text) {
		return true
	}
	return msg.ReplyMessage != nil && msg.ReplyMessage.FromID == -c.groupID
}

func (c *VKChannel) stripBotMention(text string) string {
	if c.mentionRe != nil {
		text = c.mentionRe.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(text), ",:"))
}

func (c *VKChannel) getUserName(userID int) string {
	users, err := c.vk.UsersGet(api.Params{
		"user_ids": userID,
	})
	if err != nil || len(users) == 0 {
		return strconv.Itoa(userID)
	}

	user := users[0]
	return fmt.Sprintf("%s %s", user.FirstName, user.LastName)
}

func (c *VKChannel) processAttachments(attachments []object.MessagesMessageAttachment) string {
	var parts []string

	for _, att := range attachments {
		switch att.Type {
		case "photo":
			parts = append(parts, "[photo]")
		case "video":
			parts = append(parts, "[video]")
		case "audio":
			parts = append(parts, "[audio]")
		case "doc":
			if att.Doc.Title != "" {
				parts = append(parts, fmt.Sprintf("[document: %s]", att.Doc.Title))
			} else {
				parts = append(parts, "[document]")
			}
		case "audio_message":
			parts = append(parts, "[voice]")
		case "sticker":
			parts = append(parts, "[sticker]")
		}
	}

	return strings.Join(parts, " ")
}

// VoiceCapabilities reports no voice support: the channel neither downloads
// voice messages nor uploads audio.
func (c *VKChannel) VoiceCapabilities() channels.VoiceCapabilities {
	return channels.VoiceCapabilities{}
}
