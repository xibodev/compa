//go:build whatsapp_native

// Compa - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package whatsapp

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/utils"
)

const (
	sqliteDriver   = "sqlite"
	whatsappDBName = "store.db"

	reconnectInitial    = 5 * time.Second
	reconnectMax        = 5 * time.Minute
	reconnectMultiplier = 2.0

	// sentMessagesRemembered is how many of Compa's latest sent message IDs
	// are kept to recognize their echo.
	sentMessagesRemembered = 512
)

// WhatsAppNativeChannel implements the WhatsApp channel using whatsmeow (in-process, no external bridge).
type WhatsAppNativeChannel struct {
	*channels.BaseChannel
	config       *config.WhatsAppSettings
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
	wg           sync.WaitGroup // tracks background goroutines (QR handler, reconnect)
}

// NewWhatsAppNativeChannel creates a WhatsApp channel that uses whatsmeow for connection.
// storePath is the directory for the SQLite session store (e.g. workspace/whatsapp).
func NewWhatsAppNativeChannel(
	bc *config.Channel,
	name string,
	cfg *config.WhatsAppSettings,
	bus *bus.MessageBus,
	storePath string,
) (channels.Channel, error) {
	base := channels.NewBaseChannel(name, cfg, bus, bc.AllowFrom,
		channels.WithMaxMessageLength(65536),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)
	if storePath == "" {
		storePath = "whatsapp"
	}
	c := &WhatsAppNativeChannel{
		BaseChannel: base,
		config:      cfg,
		sent:        newSentMessages(sentMessagesRemembered),
		storePath:   storePath,
	}
	return c, nil
}

func (c *WhatsAppNativeChannel) Start(ctx context.Context) error {
	logger.InfoCF("whatsapp", "Starting WhatsApp native channel (whatsmeow)", map[string]any{"store": c.storePath})

	// Reset lifecycle state from any previous Stop() so a restarted channel
	// behaves correctly.  Use reconnectMu to be consistent with eventHandler
	// and Stop() which coordinate under the same lock.
	c.reconnectMu.Lock()
	c.stopping.Store(false)
	c.reconnecting = false
	c.reconnectMu.Unlock()

	if err := os.MkdirAll(c.storePath, 0o700); err != nil {
		return fmt.Errorf("create session store dir: %w", err)
	}

	dbPath := filepath.Join(c.storePath, whatsappDBName)
	connStr := "file:" + dbPath + "?_foreign_keys=on"

	db, err := sql.Open(sqliteDriver, connStr)
	if err != nil {
		return fmt.Errorf("open whatsapp store: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	waLogger := waLog.Stdout("WhatsApp", "WARN", true)
	container := sqlstore.NewWithDB(db, sqliteDriver, waLogger)
	if err = container.Upgrade(ctx); err != nil {
		_ = db.Close()
		return fmt.Errorf("open whatsapp store: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		_ = container.Close()
		return fmt.Errorf("get device store: %w", err)
	}

	client := whatsmeow.NewClient(deviceStore, waLogger)

	// Create runCtx/runCancel BEFORE registering event handler and starting
	// goroutines so that Stop() can cancel them at any time, including during
	// the QR-login flow.
	c.runCtx, c.runCancel = context.WithCancel(ctx)

	client.AddEventHandler(c.eventHandler)

	c.mu.Lock()
	c.container = container
	c.client = client
	c.mu.Unlock()

	// cleanupOnError clears struct references and releases resources when
	// Start() fails after fields are already assigned.  This prevents
	// Stop() from operating on stale references (double-close, disconnect
	// of a partially-initialized client, or stray event handler callbacks).
	startOK := false
	defer func() {
		if startOK {
			return
		}
		c.runCancel()
		client.Disconnect()
		c.mu.Lock()
		c.client = nil
		c.container = nil
		c.mu.Unlock()
		_ = container.Close()
	}()

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(c.runCtx)
		if err != nil {
			return fmt.Errorf("get QR channel: %w", err)
		}
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
		// Handle QR events in a background goroutine so Start() returns
		// promptly.  The goroutine is tracked via c.wg and respects
		// c.runCtx for cancellation.
		// Guard wg.Add with reconnectMu + stopping check (same protocol
		// as eventHandler) so a concurrent Stop() cannot enter wg.Wait()
		// while we call wg.Add(1).
		c.reconnectMu.Lock()
		if c.stopping.Load() {
			c.reconnectMu.Unlock()
			return fmt.Errorf("channel stopped during QR setup")
		}
		c.wg.Add(1)
		c.reconnectMu.Unlock()
		go func() {
			defer c.wg.Done()
			for {
				select {
				case <-c.runCtx.Done():
					return
				case evt, ok := <-qrChan:
					if !ok {
						return
					}
					if evt.Event == "code" {
						logger.InfoCF("whatsapp", "Scan this QR code with WhatsApp (Linked Devices):", nil)
						qrterminal.GenerateWithConfig(evt.Code, qrterminal.Config{
							Level:      qrterminal.L,
							Writer:     os.Stdout,
							HalfBlocks: true,
						})
					} else {
						logger.InfoCF("whatsapp", "WhatsApp login event", map[string]any{"event": evt.Event})
					}
				}
			}
		}()
	} else {
		if err := client.Connect(); err != nil {
			return fmt.Errorf("connect: %w", err)
		}
	}

	startOK = true
	c.SetRunning(true)
	logger.InfoC("whatsapp", "WhatsApp native channel connected")
	return nil
}

func (c *WhatsAppNativeChannel) Stop(ctx context.Context) error {
	logger.InfoC("whatsapp", "Stopping WhatsApp native channel")

	// Mark as stopping under reconnectMu so the flag is visible to
	// eventHandler atomically with respect to its wg.Add(1) call.
	// This closes the TOCTOU window where eventHandler could check
	// stopping (false), then Stop sets it true + enters wg.Wait,
	// then eventHandler calls wg.Add(1) — causing a panic.
	c.reconnectMu.Lock()
	c.stopping.Store(true)
	c.reconnectMu.Unlock()

	if c.runCancel != nil {
		c.runCancel()
	}

	// Disconnect the client first so any blocking Connect()/reconnect loops
	// can be interrupted before we wait on the goroutines.
	c.mu.Lock()
	client := c.client
	container := c.container
	c.mu.Unlock()

	if client != nil {
		client.Disconnect()
	}

	// Wait for background goroutines (QR handler, reconnect) to finish in a
	// context-aware way so Stop can be bounded by ctx.
	done := make(chan struct{})
	go func() {
		c.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines have finished.
	case <-ctx.Done():
		// Context canceled or timed out; log and proceed with best-effort cleanup.
		logger.WarnC("whatsapp", fmt.Sprintf("Stop context canceled before all goroutines finished: %v", ctx.Err()))
	}

	// Now it is safe to clear and close resources.
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

func (c *WhatsAppNativeChannel) eventHandler(evt any) {
	defer channels.RecoverPanic(c.Name(), "event")
	switch v := evt.(type) {
	case *events.Message:
		c.handleIncoming(v)
	case *events.Disconnected:
		logger.InfoCF("whatsapp", "WhatsApp disconnected, will attempt reconnection", nil)
		c.reconnectMu.Lock()
		if c.reconnecting {
			c.reconnectMu.Unlock()
			return
		}
		// Check stopping while holding the lock so the check and wg.Add
		// are atomic with respect to Stop() setting the flag + calling
		// wg.Wait(). This prevents the TOCTOU race.
		if c.stopping.Load() {
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

func (c *WhatsAppNativeChannel) reconnectWithBackoff() {
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
			if backoff < reconnectMax {
				next := time.Duration(float64(backoff) * reconnectMultiplier)
				if next > reconnectMax {
					next = reconnectMax
				}
				backoff = next
			}
		}
	}
}

func (c *WhatsAppNativeChannel) handleIncoming(evt *events.Message) {
	ownPhone, ownLID := c.ownUsers()
	c.handleMessage(evt, ownPhone, ownLID)
}

// ownUsers returns the user parts of the linked account's phone-number
// address and LID; both are empty before the account is paired.
func (c *WhatsAppNativeChannel) ownUsers() (phone, lid string) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()
	if client == nil || client.Store == nil {
		return "", ""
	}
	return client.Store.GetJID().User, client.Store.GetLID().User
}

func (c *WhatsAppNativeChannel) chatsMode() string {
	if c.config == nil {
		return config.WhatsAppChatsSelf
	}
	return c.config.EffectiveChats()
}

// handleMessage decides whether a message on the linked account is input
// (see classifyChat) and publishes it. ownPhone and ownLID identify the
// account's own "message yourself" chat.
func (c *WhatsAppNativeChannel) handleMessage(evt *events.Message, ownPhone, ownLID string) {
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

	mode := c.chatsMode()
	selfChat := isOwnAddress(chat.User, chat.Server, ownPhone, ownLID)
	role := classifyChat(mode, selfChat, info.IsFromMe)
	if role == chatIgnored {
		return
	}

	content := evt.Message.GetConversation()
	if content == "" {
		content = evt.Message.GetExtendedTextMessage().GetText()
	}
	content = utils.SanitizeMessageContent(content)
	if content == "" {
		return
	}

	senderID := info.Sender.ToNonAD().String()
	chatID := chat.String()
	isGroup := chat.Server == types.GroupServer
	sender := whatsAppIdentity(senderID, info.PushName)

	metadata := map[string]string{"message_id": info.ID}
	if info.PushName != "" {
		metadata["user_name"] = info.PushName
	}
	peerKind := "direct"
	if isGroup {
		peerKind = "group"
		metadata["peer_kind"] = "group"
		metadata["peer_id"] = chatID
	} else {
		metadata["peer_kind"] = "direct"
		metadata["peer_id"] = senderID
	}

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    chatID,
		ChatType:  peerKind,
		SenderID:  senderID,
		MessageID: info.ID,
		Raw:       metadata,
	}

	if role == chatSelf {
		// The owner instructing Compa in their own chat: owner, whatever
		// allow_from says.
		inboundCtx.SenderIsOwner = true
	} else {
		// "allowed" takes only the chats the policies admit and makes no
		// pairing requests: these are the owner's own conversations, not
		// requests to Compa. In "all", the central policy decides, and may
		// record a pairing request.
		if mode == config.WhatsAppChatsAllowed && !c.Admits(peerKind, sender, chatID) {
			return
		}
		if isGroup {
			mentioned := c.isAddressed(evt.Message, ownPhone, ownLID)
			if mentioned {
				content = stripOwnMention(content, ownPhone, ownLID)
			}
			respond, cleaned := c.ShouldRespondInGroup(mentioned, content)
			if !respond {
				return
			}
			content = cleaned
			inboundCtx.Mentioned = mentioned
		}
	}

	logger.DebugCF(
		"whatsapp",
		"WhatsApp message received",
		map[string]any{"sender_id": senderID, "content_preview": utils.Truncate(content, 50)},
	)

	c.HandleInboundContext(c.runCtx, chatID, content, nil, inboundCtx, sender)
}

// isAddressed reports whether a group message is meant for Compa: it
// mentions the linked account, or it replies to a message Compa sent.
func (c *WhatsAppNativeChannel) isAddressed(msg *waE2E.Message, ownPhone, ownLID string) bool {
	ctxInfo := msg.GetExtendedTextMessage().GetContextInfo()
	if ctxInfo == nil {
		return false
	}
	for _, mentioned := range ctxInfo.GetMentionedJID() {
		jid, err := types.ParseJID(mentioned)
		if err == nil && isOwnAddress(jid.User, jid.Server, ownPhone, ownLID) {
			return true
		}
	}
	return c.sent != nil && c.sent.contains(ctxInfo.GetStanzaID())
}

// stripOwnMention removes "@<own number>" mentions, which WhatsApp writes into
// the text of a message that mentions the account.
func stripOwnMention(content, ownPhone, ownLID string) string {
	for _, user := range []string{ownPhone, ownLID} {
		if user != "" {
			content = strings.ReplaceAll(content, "@"+user, "")
		}
	}
	return strings.TrimSpace(content)
}

func (c *WhatsAppNativeChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
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

	// Detect unpaired state: the client is connected (to WhatsApp servers)
	// but has not completed QR-login yet, so sending would fail.
	if client.Store.ID == nil {
		return nil, fmt.Errorf("whatsapp not yet paired (QR login pending): %w", channels.ErrTemporary)
	}

	to, err := parseJID(msg.ChatID)
	if err != nil {
		return nil, fmt.Errorf("invalid chat id %q: %w", msg.ChatID, err)
	}

	waMsg := &waE2E.Message{
		Conversation: proto.String(msg.Content),
	}

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
