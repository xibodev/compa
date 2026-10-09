// Package whatsapp is the WhatsApp channel: Compa as a linked device of a
// WhatsApp account (whatsmeow). It answers the account's "message yourself"
// chat, and direct messages from the accounts allow_from lists, as when the
// linked account is a separate number. Groups are ignored.
package whatsapp

import (
	"context"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/media"
	"github.com/xibodev/compa/v4/pkg/utils"
)

const (
	reconnectInitial    = 5 * time.Second
	reconnectMax        = 5 * time.Minute
	reconnectMultiplier = 2.0

	// sentMessagesRemembered is how many of Compa's latest sent message IDs
	// are kept to recognize their echo.
	sentMessagesRemembered = 512

	// maxAttachmentBytes bounds a file Compa downloads.
	maxAttachmentBytes = 50 << 20
)

// WhatsAppChannel implements the WhatsApp channel with whatsmeow.
type WhatsAppChannel struct {
	*channels.BaseChannel
	sent         *sentMessages
	storePath    string
	client       *whatsmeow.Client
	container    *sqlstore.Container
	mu           sync.Mutex
	runCtx       context.Context
	runCancel    context.CancelFunc
	reconnectMu  sync.Mutex
	reconnecting bool
	stopping     atomic.Bool    // set once Stop begins; prevents new wg.Add calls
	wg           sync.WaitGroup // tracks the reconnect goroutine
	// download fetches a file of a message; tests replace it.
	download func(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error)
}

// NewWhatsAppChannel creates the WhatsApp channel. storePath is the
// directory of the linked account's session store.
func NewWhatsAppChannel(bc *config.Channel, name string, bus *bus.MessageBus, storePath string) *WhatsAppChannel {
	c := &WhatsAppChannel{
		BaseChannel: channels.NewBaseChannel(name, nil, bus, bc.AllowFrom,
			channels.WithMaxMessageLength(65536),
			channels.WithReasoningChannelID(bc.ReasoningChannelID),
		),
		sent:      newSentMessages(sentMessagesRemembered),
		storePath: storePath,
	}
	c.download = c.clientDownload
	return c
}

func (c *WhatsAppChannel) Start(ctx context.Context) error {
	logger.InfoCF("whatsapp", "Starting WhatsApp channel", map[string]any{"store": c.storePath})

	// Reset lifecycle state from any previous Stop() so a restarted channel
	// behaves correctly.
	c.reconnectMu.Lock()
	c.stopping.Store(false)
	c.reconnecting = false
	c.reconnectMu.Unlock()

	waLogger := waLog.Stdout("WhatsApp", "WARN", true)
	container, device, err := openStore(ctx, c.storePath, waLogger)
	if err != nil {
		return err
	}
	if device.ID == nil {
		// Not an error: the gateway keeps running, and connects WhatsApp once
		// it is linked and the gateway restarts.
		_ = container.Close()
		logger.WarnC("whatsapp", ErrNotLinked.Error())
		return nil
	}

	client := whatsmeow.NewClient(device, waLogger)
	c.runCtx, c.runCancel = context.WithCancel(ctx)
	client.AddEventHandler(c.eventHandler)

	c.mu.Lock()
	c.container = container
	c.client = client
	c.mu.Unlock()

	if err := client.Connect(); err != nil {
		c.runCancel()
		c.mu.Lock()
		c.client = nil
		c.container = nil
		c.mu.Unlock()
		_ = container.Close()
		return fmt.Errorf("connect: %w", err)
	}

	c.SetRunning(true)
	logger.InfoC("whatsapp", "WhatsApp channel connected")
	return nil
}

func (c *WhatsAppChannel) Stop(ctx context.Context) error {
	logger.InfoC("whatsapp", "Stopping WhatsApp channel")

	// Mark as stopping under reconnectMu so eventHandler cannot call
	// wg.Add(1) once wg.Wait has begun.
	c.reconnectMu.Lock()
	c.stopping.Store(true)
	c.reconnectMu.Unlock()

	if c.runCancel != nil {
		c.runCancel()
	}

	c.mu.Lock()
	client := c.client
	container := c.container
	c.mu.Unlock()

	if client != nil {
		client.Disconnect()
	}

	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		logger.WarnC("whatsapp", fmt.Sprintf("Stop context canceled before all goroutines finished: %v", ctx.Err()))
	}

	c.mu.Lock()
	c.client = nil
	c.container = nil
	c.mu.Unlock()

	if container != nil {
		_ = container.Close()
	}
	c.SetRunning(false)
	return nil
}

func (c *WhatsAppChannel) eventHandler(evt any) {
	defer channels.RecoverPanic(c.Name(), "event")
	switch v := evt.(type) {
	case *events.Message:
		c.handleIncoming(v)
	case *events.Disconnected:
		logger.InfoCF("whatsapp", "WhatsApp disconnected, will attempt reconnection", nil)
		c.reconnectMu.Lock()
		if c.reconnecting || c.stopping.Load() {
			c.reconnectMu.Unlock()
			return
		}
		c.reconnecting = true
		c.wg.Add(1)
		c.reconnectMu.Unlock()
		go func() {
			defer c.wg.Done()
			defer channels.RecoverPanic(c.Name(), "reconnect")
			c.reconnectWithBackoff()
		}()
	}
}

func (c *WhatsAppChannel) reconnectWithBackoff() {
	defer func() {
		c.reconnectMu.Lock()
		c.reconnecting = false
		c.reconnectMu.Unlock()
	}()

	backoff := reconnectInitial
	for {
		select {
		case <-c.runCtx.Done():
			return
		default:
		}

		c.mu.Lock()
		client := c.client
		c.mu.Unlock()
		if client == nil {
			return
		}

		logger.InfoCF("whatsapp", "WhatsApp reconnecting", map[string]any{"backoff": backoff.String()})
		err := client.Connect()
		if err == nil {
			logger.InfoC("whatsapp", "WhatsApp reconnected")
			return
		}
		logger.WarnCF("whatsapp", "WhatsApp reconnect failed", map[string]any{"error": err.Error()})

		select {
		case <-c.runCtx.Done():
			return
		case <-time.After(backoff):
			backoff = min(time.Duration(float64(backoff)*reconnectMultiplier), reconnectMax)
		}
	}
}

func (c *WhatsAppChannel) handleIncoming(evt *events.Message) {
	ownPhone, ownLID := c.ownUsers()
	c.handleMessage(evt, ownPhone, ownLID)
}

// ownUsers returns the user parts of the linked account's phone-number
// address and LID.
func (c *WhatsAppChannel) ownUsers() (phone, lid string) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || client.Store == nil {
		return "", ""
	}
	return client.Store.GetJID().User, client.Store.GetLID().User
}

// handleMessage takes a message on the linked account as input when it is
// the owner writing in their "message yourself" chat, or a direct message
// from an account allow_from lists. Its files are downloaded only then.
func (c *WhatsAppChannel) handleMessage(evt *events.Message, ownPhone, ownLID string) {
	if evt == nil || evt.Message == nil {
		return
	}
	info := evt.Info
	chat := info.Chat
	// Status updates, broadcast lists and channels are not conversations.
	if chat.Server == types.BroadcastServer || chat.Server == types.NewsletterServer {
		return
	}
	// Compa's own replies come back as the owner's messages.
	if c.sent != nil && c.sent.contains(info.ID) {
		return
	}

	selfChat := isOwnAddress(chat.User, chat.Server, ownPhone, ownLID)
	role := classifyChat(selfChat, info.IsFromMe)
	if role == chatIgnored {
		return
	}

	senderJID := info.Sender.ToNonAD()
	senderID := senderJID.String()
	chatID := chat.String()
	chatType := "direct"
	if chat.Server == types.GroupServer {
		chatType = "group"
	}
	sender := whatsAppIdentity(senderJID.User, senderJID.Server, info.PushName)
	// The owner's contacts write to the owner, not to Compa: only the
	// accounts allow_from lists are input, and no pairing request is made.
	if role == chatOther && !c.Admits(chatType, sender, chatID) {
		return
	}

	text, files := messageParts(evt.Message)
	text = utils.SanitizeMessageContent(text)
	if text == "" && len(files) == 0 {
		return
	}

	metadata := map[string]string{"message_id": info.ID, "peer_kind": "direct", "peer_id": senderID}
	if info.PushName != "" {
		metadata["user_name"] = info.PushName
	}
	inboundCtx := bus.InboundContext{
		Channel:       c.Name(),
		ChatID:        chatID,
		ChatType:      chatType,
		SenderID:      senderID,
		MessageID:     info.ID,
		Raw:           metadata,
		SenderIsOwner: role == chatSelf,
	}

	attachments := make([]channels.Attachment, 0, len(files))
	for i, f := range files {
		attachments = append(attachments, channels.Attachment{
			Kind:        f.kind,
			Name:        f.name,
			ContentType: f.mime,
			Download: func(ctx context.Context) (string, error) {
				return c.saveFile(ctx, f, fmt.Sprintf("%s-%d", info.ID, i))
			},
		})
	}
	_ = c.Receive(c.runCtx, chatID, text, attachments, inboundCtx, sender)
}

// waFile is a file a message carries.
type waFile struct {
	kind, name, mime string
	size             uint64
	msg              whatsmeow.DownloadableMessage
}

// messageParts returns a message's text and the files it carries.
func messageParts(msg *waE2E.Message) (string, []waFile) {
	if doc := msg.GetDocumentWithCaptionMessage().GetMessage(); doc != nil {
		msg = doc
	}
	text := msg.GetConversation()
	if text == "" {
		text = msg.GetExtendedTextMessage().GetText()
	}
	var files []waFile
	if m := msg.GetImageMessage(); m != nil {
		files = append(files, waFile{kind: "image", mime: m.GetMimetype(), size: m.GetFileLength(), msg: m})
		text = joinText(text, m.GetCaption())
	}
	if m := msg.GetStickerMessage(); m != nil {
		files = append(files, waFile{kind: "image", mime: m.GetMimetype(), size: m.GetFileLength(), msg: m})
	}
	if m := msg.GetVideoMessage(); m != nil {
		files = append(files, waFile{kind: "video", mime: m.GetMimetype(), size: m.GetFileLength(), msg: m})
		text = joinText(text, m.GetCaption())
	}
	if m := msg.GetAudioMessage(); m != nil {
		kind := "audio"
		if m.GetPTT() {
			kind = "voice"
		}
		files = append(files, waFile{kind: kind, mime: m.GetMimetype(), size: m.GetFileLength(), msg: m})
	}
	if m := msg.GetDocumentMessage(); m != nil {
		files = append(files, waFile{
			kind: "file", name: filepath.Base(m.GetFileName()), mime: m.GetMimetype(), size: m.GetFileLength(), msg: m,
		})
		text = joinText(text, m.GetCaption())
	}
	return text, files
}

func joinText(text, more string) string {
	if more == "" {
		return text
	}
	if text == "" {
		return more
	}
	return text + "\n" + more
}

// saveFile downloads f into the media temp dir.
func (c *WhatsAppChannel) saveFile(ctx context.Context, f waFile, base string) (string, error) {
	if f.size > maxAttachmentBytes {
		return "", fmt.Errorf("%d bytes is over the %d MB limit", f.size, maxAttachmentBytes>>20)
	}
	data, err := c.download(ctx, f.msg)
	if err != nil {
		return "", err
	}
	name := f.name
	if name == "" || name == "." {
		name = base
		if exts, _ := mime.ExtensionsByType(f.mime); len(exts) > 0 {
			name += exts[0]
		}
	}
	dir := media.TempDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, "whatsapp-*-"+name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func (c *WhatsAppChannel) clientDownload(ctx context.Context, msg whatsmeow.DownloadableMessage) ([]byte, error) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil {
		return nil, channels.ErrNotRunning
	}
	return client.Download(ctx, msg)
}

func (c *WhatsAppChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || !client.IsConnected() {
		return nil, fmt.Errorf("whatsapp connection not established: %w", channels.ErrTemporary)
	}

	to, err := parseJID(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat id %q: %w", msg.ChatID, err)
	}
	waMsg := &waE2E.Message{Conversation: proto.String(msg.Content)}

	// Remember the ID before sending: the echo of this message on the linked
	// account may arrive before SendMessage returns.
	id := client.GenerateMessageID()
	c.sent.add(id)
	if _, err = client.SendMessage(ctx, to, waMsg, whatsmeow.SendRequestExtra{ID: id}); err != nil {
		return nil, fmt.Errorf("whatsapp send: %w", channels.ErrTemporary)
	}
	return nil, nil
}

// parseJID converts a chat ID (phone number or JID string) to types.JID.
func parseJID(s string) (types.JID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return types.JID{}, fmt.Errorf("empty chat id")
	}
	if strings.Contains(s, "@") {
		return types.ParseJID(s)
	}
	return types.NewJID(s, types.DefaultUserServer), nil
}
