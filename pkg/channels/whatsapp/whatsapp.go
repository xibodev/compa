package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/channels"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/identity"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/utils"
)

const (
	// bridgeReadLimit bounds one message from the bridge.
	bridgeReadLimit        = 4 << 20
	bridgeHandshakeTimeout = 10 * time.Second
	redialInitial          = time.Second
	redialMax              = time.Minute
)

type WhatsAppChannel struct {
	*channels.BaseChannel
	conn      *websocket.Conn
	config    *config.WhatsAppSettings
	url       string
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	connected bool
	// maxMediaBytes bounds a media file the bridge hands over.
	maxMediaBytes int64
}

func NewWhatsAppChannel(
	bc *config.Channel,
	cfg *config.WhatsAppSettings,
	bus *bus.MessageBus,
) (*WhatsAppChannel, error) {
	base := channels.NewBaseChannel(
		"whatsapp",
		cfg,
		bus,
		bc.AllowFrom,
		channels.WithMaxMessageLength(65536),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	return &WhatsAppChannel{
		BaseChannel:   base,
		config:        cfg,
		url:           cfg.BridgeURL,
		connected:     false,
		maxMediaBytes: config.DefaultMaxMediaSize,
	}, nil
}

func (c *WhatsAppChannel) Start(ctx context.Context) error {
	logger.InfoCF("whatsapp", "Starting WhatsApp channel", map[string]any{
		"bridge_url": c.url,
	})

	if err := checkBridgeURL(ctx, c.url); err != nil {
		return err
	}

	c.ctx, c.cancel = context.WithCancel(ctx)

	conn, err := c.dial(c.ctx)
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to connect to WhatsApp bridge: %w", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.connected = true
	c.mu.Unlock()

	c.SetRunning(true)
	logger.InfoC("whatsapp", "WhatsApp channel connected")

	go c.listen()

	return nil
}

// dial connects to the bridge with a dialer of its own; changing the
// package-level websocket.DefaultDialer would change every other user's too.
func (c *WhatsAppChannel) dial(ctx context.Context) (*websocket.Conn, error) {
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: bridgeHandshakeTimeout,
	}
	conn, resp, err := dialer.DialContext(ctx, c.url, nil)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(bridgeReadLimit)
	return conn, nil
}

// checkBridgeURL refuses an unencrypted ws:// bridge outside this computer and
// its local network: the user's messages would cross the internet in clear
// text, and anyone on the way could inject messages. wss:// is accepted.
func checkBridgeURL(ctx context.Context, rawURL string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid WhatsApp bridge_url: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "wss":
		return nil
	case "ws":
	default:
		return fmt.Errorf("WhatsApp bridge_url must start with ws:// or wss://")
	}

	host := u.Hostname()
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("resolve WhatsApp bridge host %q: %w", host, err)
		}
		for _, addr := range addrs {
			ips = append(ips, addr.IP)
		}
	}
	for _, ip := range ips {
		if !isLocalNetworkIP(ip) {
			return fmt.Errorf(
				"WhatsApp bridge_url %q uses unencrypted ws:// to a host outside this computer and its local network; use wss://",
				rawURL,
			)
		}
	}
	return nil
}

// sharedAddressSpace is 100.64.0.0/10 (RFC 6598), used by carrier NAT and by
// overlay networks such as Tailscale; it is not reachable from the internet.
var sharedAddressSpace = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isLocalNetworkIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || sharedAddressSpace.Contains(ip)
}

func (c *WhatsAppChannel) Stop(ctx context.Context) error {
	logger.InfoC("whatsapp", "Stopping WhatsApp channel...")

	// Cancel context first to signal listen goroutine to exit
	if c.cancel != nil {
		c.cancel()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		if err := c.conn.Close(); err != nil {
			logger.ErrorCF("whatsapp", "Error closing WhatsApp connection", map[string]any{
				"error": err.Error(),
			})
		}
		c.conn = nil
	}

	c.connected = false
	c.SetRunning(false)

	return nil
}

func (c *WhatsAppChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	// Check ctx before acquiring lock
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return nil, fmt.Errorf("whatsapp connection not established: %w", channels.ErrTemporary)
	}

	payload := map[string]any{
		"type":    "message",
		"to":      msg.ChatID,
		"content": msg.Content,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal message: %w", err)
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		_ = c.conn.SetWriteDeadline(time.Time{})
		return nil, fmt.Errorf("whatsapp send: %w", channels.ErrTemporary)
	}
	_ = c.conn.SetWriteDeadline(time.Time{})

	return nil, nil
}

// listen reads the bridge's messages. After a read error the connection is
// dead (gorilla/websocket panics if it is read again), so it is closed and the
// bridge redialed with backoff.
func (c *WhatsAppChannel) listen() {
	for {
		if c.ctx.Err() != nil {
			return
		}

		c.mu.Lock()
		conn := c.conn
		c.mu.Unlock()

		if conn == nil {
			if !c.redial() {
				return
			}
			continue
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			logger.ErrorCF("whatsapp", "WhatsApp read error, reconnecting", map[string]any{
				"error": err.Error(),
			})
			c.dropConn(conn)
			continue
		}

		c.handleRawMessage(message)
	}
}

// dropConn closes a failed connection unless Stop or a redial replaced it.
func (c *WhatsAppChannel) dropConn(conn *websocket.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == conn {
		_ = c.conn.Close()
		c.conn = nil
		c.connected = false
	}
}

// redial reconnects to the bridge, waiting longer after each failure. It
// returns false when the channel stops.
func (c *WhatsAppChannel) redial() bool {
	backoff := redialInitial
	for {
		select {
		case <-c.ctx.Done():
			return false
		case <-time.After(backoff):
		}

		conn, err := c.dial(c.ctx)
		if err == nil {
			c.mu.Lock()
			if c.ctx.Err() != nil {
				c.mu.Unlock()
				_ = conn.Close()
				return false
			}
			c.conn = conn
			c.connected = true
			c.mu.Unlock()
			logger.InfoC("whatsapp", "WhatsApp bridge reconnected")
			return true
		}

		logger.WarnCF("whatsapp", "WhatsApp bridge reconnect failed", map[string]any{
			"error":   err.Error(),
			"backoff": backoff.String(),
		})
		backoff = min(backoff*2, redialMax)
	}
}

func (c *WhatsAppChannel) handleRawMessage(message []byte) {
	defer channels.RecoverPanic(c.Name(), "message")

	var msg map[string]any
	if err := json.Unmarshal(message, &msg); err != nil {
		logger.ErrorCF("whatsapp", "Failed to unmarshal WhatsApp message", map[string]any{
			"error": err.Error(),
		})
		return
	}

	if msgType, ok := msg["type"].(string); ok && msgType == "message" {
		c.handleIncomingMessage(msg)
	}
}

func (c *WhatsAppChannel) handleIncomingMessage(msg map[string]any) {
	senderID, ok := msg["from"].(string)
	if !ok {
		return
	}

	chatID, ok := msg["chat"].(string)
	if !ok {
		chatID = senderID
	}

	content, ok := msg["content"].(string)
	if !ok {
		content = ""
	}

	metadata := make(map[string]string)
	var messageID string
	if mid, ok := msg["id"].(string); ok {
		messageID = mid
	}
	if userName, ok := msg["from_name"].(string); ok {
		metadata["user_name"] = userName
	}

	logger.InfoCF("whatsapp", "WhatsApp message received", map[string]any{
		"sender":  senderID,
		"preview": utils.Truncate(content, 50),
	})

	sender := bus.SenderInfo{
		Platform:    "whatsapp",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("whatsapp", senderID),
	}
	if display, ok := metadata["user_name"]; ok {
		sender.DisplayName = display
	}

	inboundCtx := bus.InboundContext{
		Channel:   c.Name(),
		ChatID:    chatID,
		ChatType:  "direct",
		SenderID:  senderID,
		MessageID: messageID,
		Raw:       metadata,
	}
	if chatID != senderID {
		inboundCtx.ChatType = "group"
	}

	// Decide on the message before taking its media. A direct message the
	// policy rejects still goes to it, as text only, so that an unpaired
	// sender is recorded for the owner to approve.
	if !c.Admits(inboundCtx.ChatType, sender, chatID) {
		if inboundCtx.ChatType == "direct" {
			text := content
			if text == "" {
				text = "[media]"
			}
			c.HandleInboundContext(c.ctx, chatID, text, nil, inboundCtx, sender)
		}
		return
	}

	if inboundCtx.ChatType == "group" {
		// The bridge reports no mentions, so a group message gets an answer
		// only through a prefix, or when the group trigger asks for none.
		respond, cleaned := c.ShouldRespondInGroup(false, content)
		if !respond {
			return
		}
		content = cleaned
	}

	var mediaPaths []string
	if mediaData, ok := msg["media"].([]any); ok {
		mediaPaths = make([]string, 0, len(mediaData))
		for _, m := range mediaData {
			p, ok := m.(string)
			if !ok {
				continue
			}
			if safe, err := bridgeMediaPath(p, c.maxMediaBytes); err == nil {
				mediaPaths = append(mediaPaths, safe)
			} else {
				logger.WarnCF("whatsapp", "Ignoring media path from the bridge", map[string]any{
					"path":  p,
					"error": err.Error(),
				})
			}
		}
	}

	c.HandleInboundContext(c.ctx, chatID, content, mediaPaths, inboundCtx, sender)
}

// bridgeMediaPath accepts a media file the bridge names only when it is a
// regular file, within the size limit, in the system temp directory, where
// bridges put the media they download. Anything else would let the bridge,
// or whoever speaks for it, make the agent read any file on this computer.
func bridgeMediaPath(p string, maxBytes int64) (string, error) {
	if p == "" || !filepath.IsAbs(p) {
		return "", errors.New("not an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	tempDir := os.TempDir()
	if real, err := filepath.EvalSymlinks(tempDir); err == nil {
		tempDir = real
	}
	rel, err := filepath.Rel(tempDir, resolved)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(rel) {
		return "", errors.New("outside the temp directory")
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	if maxBytes > 0 && info.Size() > maxBytes {
		return "", fmt.Errorf("larger than %d bytes", maxBytes)
	}
	return resolved, nil
}
