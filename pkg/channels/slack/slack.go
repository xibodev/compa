package slack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/channels"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/identity"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/media"
	"github.com/xibodev/compa/pkg/utils"
)

// classifySendError maps a failed request to the channel error sentinels: a
// rate limit is retried after the pause Slack asks for, anything else with
// backoff.
func classifySendError(err error) error {
	var limited *slack.RateLimitedError
	if errors.As(err, &limited) {
		return channels.NewRateLimitError(limited.RetryAfter, err)
	}
	return fmt.Errorf("%w: %w", channels.ErrTemporary, err)
}

type SlackChannel struct {
	*channels.BaseChannel
	config       *config.SlackSettings
	api          *slack.Client
	socketClient *socketmode.Client
	botUserID    string
	teamID       string
	ctx          context.Context
	cancel       context.CancelFunc
	pendingAcks  sync.Map
	uploadFileFn func(context.Context, slack.UploadFileParameters) error
	postTextFn   func(context.Context, string, string, string) error
}

type slackMessageRef struct {
	ChannelID string
	Timestamp string
}

func NewSlackChannel(
	bc *config.Channel,
	cfg *config.SlackSettings,
	messageBus *bus.MessageBus,
) (*SlackChannel, error) {
	if cfg.BotToken.String() == "" || cfg.AppToken.String() == "" {
		return nil, fmt.Errorf("slack bot_token and app_token are required")
	}

	api := slack.New(
		cfg.BotToken.String(),
		slack.OptionAppLevelToken(cfg.AppToken.String()),
	)

	socketClient := socketmode.New(api)

	base := channels.NewBaseChannel("slack", cfg, messageBus, bc.AllowFrom,
		channels.WithMaxMessageLength(40000),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	return &SlackChannel{
		BaseChannel:  base,
		config:       cfg,
		api:          api,
		socketClient: socketClient,
		uploadFileFn: func(ctx context.Context, params slack.UploadFileParameters) error {
			_, err := api.UploadFileContext(ctx, params)
			return err
		},
		postTextFn: func(ctx context.Context, channelID, threadTS, text string) error {
			opts := []slack.MsgOption{slack.MsgOptionText(text, true)}
			if threadTS != "" {
				opts = append(opts, slack.MsgOptionTS(threadTS))
			}
			_, _, err := api.PostMessageContext(ctx, channelID, opts...)
			return err
		},
	}, nil
}

func (c *SlackChannel) Start(ctx context.Context) error {
	logger.InfoC("slack", "Starting Slack channel (Socket Mode)")

	c.ctx, c.cancel = context.WithCancel(ctx)

	authResp, err := c.api.AuthTest()
	if err != nil {
		return fmt.Errorf("slack auth test failed: %w", err)
	}
	c.botUserID = authResp.UserID
	c.teamID = authResp.TeamID

	logger.InfoCF("slack", "Slack bot connected", map[string]any{
		"bot_user_id": c.botUserID,
		"team":        authResp.Team,
	})

	go c.eventLoop()

	go func() {
		if err := c.socketClient.RunContext(c.ctx); err != nil {
			if c.ctx.Err() == nil {
				logger.ErrorCF("slack", "Socket Mode connection error", map[string]any{
					"error": err.Error(),
				})
			}
		}
	}()

	c.SetRunning(true)
	logger.InfoC("slack", "Slack channel started (Socket Mode)")
	return nil
}

func (c *SlackChannel) Stop(ctx context.Context) error {
	logger.InfoC("slack", "Stopping Slack channel")

	if c.cancel != nil {
		c.cancel()
	}

	c.SetRunning(false)
	logger.InfoC("slack", "Slack channel stopped")
	return nil
}

func (c *SlackChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	deliveryChatID, channelID, threadTS := resolveSlackOutboundTarget(msg.ChatID, &msg.Context)
	if channelID == "" {
		return nil, fmt.Errorf("invalid slack chat ID: %s", msg.ChatID)
	}

	opts := []slack.MsgOption{
		// Escaped: the text comes from the model, and Slack would otherwise
		// turn "<!channel>", "<!here>" or "<@U…>" in it into pings.
		slack.MsgOptionText(msg.Content, true),
	}

	if msg.ReplyToMessageID != "" && threadTS == "" {
		// Answer to the message by creating a Thread under it
		opts = append(opts, slack.MsgOptionTS(msg.ReplyToMessageID))
	} else if threadTS != "" {
		// If we are already in a thread, continue in the thread
		opts = append(opts, slack.MsgOptionTS(threadTS))
	}

	_, ts, err := c.api.PostMessageContext(ctx, channelID, opts...)
	if err != nil {
		return nil, fmt.Errorf("slack send: %w", classifySendError(err))
	}

	if ref, ok := c.pendingAcks.LoadAndDelete(deliveryChatID); ok {
		msgRef, ok := ref.(slackMessageRef)
		if !ok {
			return []string{ts}, nil
		}
		c.api.AddReaction("white_check_mark", slack.ItemRef{
			Channel:   msgRef.ChannelID,
			Timestamp: msgRef.Timestamp,
		})
	}

	logger.DebugCF("slack", "Message sent", map[string]any{
		"channel_id": channelID,
		"thread_ts":  threadTS,
	})

	return []string{ts}, nil
}

// SendMedia implements the channels.MediaSender interface.
func (c *SlackChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	_, channelID, threadTS := resolveSlackMediaOutboundTarget(msg.ChatID, &msg.Context)
	if channelID == "" {
		return nil, fmt.Errorf("invalid slack chat ID: %s", msg.ChatID)
	}

	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	caption := slackFirstMediaCaption(msg.Parts)
	sentAny := false
	for _, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("slack", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		filename := part.Filename
		if filename == "" {
			filename = "file"
		}

		title := part.Caption
		if title == "" {
			title = filename
		}

		err = c.uploadFileFn(ctx, slack.UploadFileParameters{
			Channel:         channelID,
			ThreadTimestamp: threadTS,
			File:            localPath,
			Filename:        filename,
			Title:           title,
		})
		if err != nil {
			logger.ErrorCF("slack", "Failed to upload media", map[string]any{
				"filename": filename,
				"error":    err.Error(),
			})
			return nil, fmt.Errorf("slack send media: %w", classifySendError(err))
		}
		sentAny = true
	}

	if sentAny && caption != "" {
		if err := c.postTextFn(ctx, channelID, threadTS, caption); err != nil {
			return nil, fmt.Errorf("slack send media caption fallback: %w", channels.ErrTemporary)
		}
	}

	// UploadFile does not expose the posted message timestamp in its
	// response; returning nil avoids conflating file IDs with message IDs.
	return nil, nil
}

func slackFirstMediaCaption(parts []bus.MediaPart) string {
	for _, part := range parts {
		if caption := strings.TrimSpace(part.Caption); caption != "" {
			return caption
		}
	}
	return ""
}

// ReactToMessage implements channels.ReactionCapable.
// It adds an "eyes" (👀) reaction to the inbound message and returns an undo function
// that removes the reaction.
func (c *SlackChannel) ReactToMessage(ctx context.Context, chatID, messageID string) (func(), error) {
	channelID, _ := parseSlackChatID(chatID)
	if channelID == "" {
		return func() {}, nil
	}

	c.api.AddReaction("eyes", slack.ItemRef{
		Channel:   channelID,
		Timestamp: messageID,
	})

	return func() {
		c.api.RemoveReaction("eyes", slack.ItemRef{
			Channel:   channelID,
			Timestamp: messageID,
		})
	}, nil
}

func (c *SlackChannel) eventLoop() {
	for {
		select {
		case <-c.ctx.Done():
			return
		case event, ok := <-c.socketClient.Events:
			if !ok {
				return
			}
			switch event.Type {
			case socketmode.EventTypeEventsAPI:
				c.handleEventsAPI(event)
			case socketmode.EventTypeSlashCommand:
				c.handleSlashCommand(event)
			case socketmode.EventTypeInteractive:
				if event.Request != nil {
					c.socketClient.Ack(*event.Request)
				}
			}
		}
	}
}

func (c *SlackChannel) handleEventsAPI(event socketmode.Event) {
	defer channels.RecoverPanic(c.Name(), "event")
	if event.Request != nil {
		c.socketClient.Ack(*event.Request)
	}

	eventsAPIEvent, ok := event.Data.(slackevents.EventsAPIEvent)
	if !ok {
		return
	}

	switch ev := eventsAPIEvent.InnerEvent.Data.(type) {
	case *slackevents.MessageEvent:
		c.handleMessageEvent(ev)
	case *slackevents.AppMentionEvent:
		c.handleAppMention(ev)
	}
}

func (c *SlackChannel) handleMessageEvent(ev *slackevents.MessageEvent) {
	if ev.User == c.botUserID || ev.User == "" {
		return
	}
	if ev.BotID != "" {
		return
	}
	if ev.SubType != "" && ev.SubType != "file_share" {
		return
	}

	senderID := ev.User
	sender := bus.SenderInfo{
		Platform:    "slack",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("slack", senderID),
	}
	channelID := ev.Channel
	threadTS := ev.ThreadTimeStamp
	messageTS := ev.TimeStamp

	chatID := channelID
	if threadTS != "" {
		chatID = channelID + "/" + threadTS
	}

	peerKind := "channel"
	if strings.HasPrefix(channelID, "D") {
		peerKind = "direct"
	}

	// A channel message that mentions the bot also arrives as an app_mention
	// event, which answers it; answering here too would reply twice.
	if peerKind != "direct" && c.botUserID != "" && strings.Contains(ev.Text, "<@"+c.botUserID+">") {
		return
	}

	// Decide on the message before downloading its files. A direct message
	// the policy rejects still goes to it, as text only, so that an unpaired
	// sender is recorded for the owner to approve.
	if !c.Admits(peerKind, sender, channelID) {
		logger.DebugCF("slack", "Message not admitted by the access policy", map[string]any{
			"user_id": senderID,
		})
		if peerKind == "direct" {
			content := c.stripBotMention(ev.Text)
			if strings.TrimSpace(content) == "" {
				content = "[file]"
			}
			c.HandleInboundContext(c.ctx, chatID, content, nil, bus.InboundContext{
				Channel:   c.Name(),
				Account:   c.teamID,
				ChatID:    channelID,
				ChatType:  peerKind,
				SenderID:  senderID,
				MessageID: messageTS,
			}, sender)
		}
		return
	}

	c.pendingAcks.Store(chatID, slackMessageRef{
		ChannelID: channelID,
		Timestamp: messageTS,
	})

	content := ev.Text
	content = c.stripBotMention(content)

	// In non-DM channels, apply group trigger filtering
	if !strings.HasPrefix(channelID, "D") {
		respond, cleaned := c.ShouldRespondInGroup(false, content)
		if !respond {
			return
		}
		content = cleaned
	}

	var mediaPaths []string

	scope := channels.BuildMediaScope("slack", chatID, messageTS)

	// Helper to register a local file with the media store
	storeMedia := func(localPath, filename string) string {
		if store := c.GetMediaStore(); store != nil {
			ref, err := store.Store(localPath, media.MediaMeta{
				Filename:      filename,
				Source:        "slack",
				CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
			}, scope)
			if err == nil {
				return ref
			}
		}
		return localPath // fallback
	}

	if ev.Message != nil && len(ev.Message.Files) > 0 {
		for _, file := range ev.Message.Files {
			localPath := fetchFile(c, file)
			if localPath == "" {
				continue
			}
			mediaPaths = append(mediaPaths, storeMedia(localPath, file.Name))
			content += fmt.Sprintf("\n[file: %s]", file.Name)
		}
	}

	if strings.TrimSpace(content) == "" {
		return
	}

	metadata := map[string]string{
		"message_ts": messageTS,
		"channel_id": channelID,
		"thread_ts":  threadTS,
		"platform":   "slack",
		"team_id":    c.teamID,
	}

	logger.DebugCF("slack", "Received message", map[string]any{
		"sender_id":  senderID,
		"chat_id":    chatID,
		"preview":    utils.Truncate(content, 50),
		"has_thread": threadTS != "",
	})

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		Account:   c.teamID,
		ChatID:    channelID,
		ChatType:  peerKind,
		SenderID:  senderID,
		MessageID: messageTS,
		SpaceID:   c.teamID,
		SpaceType: "workspace",
		Raw:       metadata,
	}
	if threadTS != "" {
		inboundCtx.TopicID = threadTS
	}

	c.HandleInboundContext(c.ctx, chatID, content, mediaPaths, inboundCtx, sender)
}

func (c *SlackChannel) handleAppMention(ev *slackevents.AppMentionEvent) {
	if ev.User == c.botUserID {
		return
	}

	senderID := ev.User
	mentionSender := bus.SenderInfo{
		Platform:    "slack",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("slack", senderID),
	}
	channelID := ev.Channel
	threadTS := ev.ThreadTimeStamp
	messageTS := ev.TimeStamp

	mentionPeerKind := "channel"
	if strings.HasPrefix(channelID, "D") {
		mentionPeerKind = "direct"
	}
	// Mentions arrive from channels; the message event handles direct
	// messages, including recording unpaired senders.
	if !c.Admits(mentionPeerKind, mentionSender, channelID) {
		logger.DebugCF("slack", "Mention not admitted by the access policy", map[string]any{
			"user_id": ev.User,
		})
		return
	}

	var chatID string
	if threadTS != "" {
		chatID = channelID + "/" + threadTS
	} else {
		chatID = channelID + "/" + messageTS
	}

	c.pendingAcks.Store(chatID, slackMessageRef{
		ChannelID: channelID,
		Timestamp: messageTS,
	})

	content := c.stripBotMention(ev.Text)

	if strings.TrimSpace(content) == "" {
		return
	}

	metadata := map[string]string{
		"message_ts": messageTS,
		"channel_id": channelID,
		"thread_ts":  threadTS,
		"platform":   "slack",
		"is_mention": "true",
		"team_id":    c.teamID,
	}
	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		Account:   c.teamID,
		ChatID:    channelID,
		ChatType:  mentionPeerKind,
		TopicID:   threadTS,
		SenderID:  senderID,
		MessageID: messageTS,
		SpaceID:   c.teamID,
		SpaceType: "workspace",
		Mentioned: true,
		Raw:       metadata,
	}

	c.HandleInboundContext(c.ctx, chatID, content, nil, inboundCtx, mentionSender)
}

func (c *SlackChannel) handleSlashCommand(event socketmode.Event) {
	defer channels.RecoverPanic(c.Name(), "slash command")
	cmd, ok := event.Data.(slack.SlashCommand)
	if !ok {
		return
	}

	if event.Request != nil {
		c.socketClient.Ack(*event.Request)
	}

	cmdSender := bus.SenderInfo{
		Platform:    "slack",
		PlatformID:  cmd.UserID,
		CanonicalID: identity.BuildCanonicalID("slack", cmd.UserID),
	}
	// A command carries no files, so the access policy in
	// HandleInboundContext decides on it, and records an unpaired sender.

	senderID := cmd.UserID
	channelID := cmd.ChannelID
	chatID := channelID
	content := cmd.Text

	if strings.TrimSpace(content) == "" {
		content = "help"
	}

	metadata := map[string]string{
		"channel_id": channelID,
		"platform":   "slack",
		"is_command": "true",
		"trigger_id": cmd.TriggerID,
		"team_id":    c.teamID,
	}

	logger.DebugCF("slack", "Slash command received", map[string]any{
		"sender_id": senderID,
		"command":   cmd.Command,
		"text":      utils.Truncate(content, 50),
	})
	peerKind := "channel"
	if strings.HasPrefix(channelID, "D") {
		peerKind = "direct"
	}
	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		Account:   c.teamID,
		ChatID:    channelID,
		ChatType:  peerKind,
		SenderID:  senderID,
		SpaceID:   c.teamID,
		SpaceType: "workspace",
		Raw:       metadata,
	}

	c.HandleInboundContext(c.ctx, chatID, content, nil, inboundCtx, cmdSender)
}

// fetchFile downloads a shared file; tests replace it.
var fetchFile = (*SlackChannel).downloadSlackFile

func (c *SlackChannel) downloadSlackFile(file slack.File) string {
	downloadURL := file.URLPrivateDownload
	if downloadURL == "" {
		downloadURL = file.URLPrivate
	}
	if downloadURL == "" {
		logger.ErrorCF("slack", "No download URL for file", map[string]any{"file_id": file.ID})
		return ""
	}

	return utils.DownloadFile(downloadURL, file.Name, utils.DownloadOptions{
		LoggerPrefix: "slack",
		ExtraHeaders: map[string]string{
			"Authorization": "Bearer " + c.config.BotToken.String(),
		},
	})
}

func (c *SlackChannel) stripBotMention(text string) string {
	mention := fmt.Sprintf("<@%s>", c.botUserID)
	text = strings.ReplaceAll(text, mention, "")
	return strings.TrimSpace(text)
}

func parseSlackChatID(chatID string) (channelID, threadTS string) {
	parts := strings.SplitN(chatID, "/", 2)
	channelID = parts[0]
	if len(parts) > 1 {
		threadTS = parts[1]
	}
	return channelID, threadTS
}

func resolveSlackOutboundTarget(chatID string, outboundCtx *bus.InboundContext) (string, string, string) {
	deliveryChatID := strings.TrimSpace(chatID)
	if deliveryChatID == "" && outboundCtx != nil {
		deliveryChatID = strings.TrimSpace(outboundCtx.ChatID)
	}
	channelID, threadTS := parseSlackChatID(deliveryChatID)
	if threadTS == "" && outboundCtx != nil {
		threadTS = strings.TrimSpace(outboundCtx.TopicID)
		if threadTS != "" && channelID != "" {
			deliveryChatID = channelID + "/" + threadTS
		}
	}
	return deliveryChatID, channelID, threadTS
}

func resolveSlackMediaOutboundTarget(chatID string, outboundCtx *bus.InboundContext) (string, string, string) {
	deliveryChatID := strings.TrimSpace(chatID)
	if deliveryChatID == "" && outboundCtx != nil {
		deliveryChatID = strings.TrimSpace(outboundCtx.ChatID)
	}
	channelID, threadTS := parseSlackChatID(deliveryChatID)
	if threadTS == "" && outboundCtx != nil {
		threadTS = strings.TrimSpace(outboundCtx.TopicID)
		if threadTS != "" && channelID != "" {
			deliveryChatID = channelID + "/" + threadTS
		}
	}
	return deliveryChatID, channelID, threadTS
}
