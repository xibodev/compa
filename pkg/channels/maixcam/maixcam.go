package maixcam

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/pkg/bus"
	"github.com/xibodev/compa/pkg/channels"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/identity"
	"github.com/xibodev/compa/pkg/logger"
)

const (
	// The channel listens on this computer only, on a port of its own,
	// unless the settings say otherwise.
	defaultHost = "127.0.0.1"
	defaultPort = 18792

	// maxMessageBytes bounds one JSON message from a device.
	maxMessageBytes = 64 << 10
	// idleTimeout closes a connection that sends nothing for this long.
	idleTimeout = 5 * time.Minute
	// maxConnections bounds how many devices may be connected at once.
	maxConnections = 16
	writeTimeout   = 10 * time.Second
)

var errMessageTooLarge = errors.New("maixcam message too large")

type MaixCamChannel struct {
	*channels.BaseChannel
	config     *config.MaixCamSettings
	listener   net.Listener
	ctx        context.Context
	cancel     context.CancelFunc
	clients    map[net.Conn]string // connection -> chat ID (the device's address)
	clientsMux sync.RWMutex
	writeMu    sync.Mutex
}

type MaixCamMessage struct {
	Type string `json:"type"`
	// Token must equal settings.token when one is set.
	Token     string         `json:"token,omitempty"`
	Tips      string         `json:"tips"`
	Timestamp float64        `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

func NewMaixCamChannel(
	bc *config.Channel,
	cfg *config.MaixCamSettings,
	bus *bus.MessageBus,
) (*MaixCamChannel, error) {
	base := channels.NewBaseChannel(
		"maixcam",
		cfg,
		bus,
		bc.AllowFrom,
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	return &MaixCamChannel{
		BaseChannel: base,
		config:      cfg,
		clients:     make(map[net.Conn]string),
	}, nil
}

// listenAddress is the configured host and port, or the defaults.
func (c *MaixCamChannel) listenAddress() string {
	host := strings.TrimSpace(c.config.Host)
	if host == "" {
		host = defaultHost
	}
	port := c.config.Port
	if port <= 0 {
		port = defaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

func (c *MaixCamChannel) Start(ctx context.Context) error {
	logger.InfoC("maixcam", "Starting MaixCam channel server")

	c.ctx, c.cancel = context.WithCancel(ctx)

	addr := c.listenAddress()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	c.listener = listener
	c.SetRunning(true)

	fields := map[string]any{"address": addr}
	if c.config.Token.String() == "" {
		fields["hint"] = "Set settings.token so that only devices that send it are accepted."
	}
	logger.InfoCF("maixcam", "MaixCam server listening", fields)

	go c.acceptConnections()

	return nil
}

func (c *MaixCamChannel) acceptConnections() {
	logger.DebugC("maixcam", "Starting connection acceptor")

	for {
		select {
		case <-c.ctx.Done():
			logger.InfoC("maixcam", "Stopping connection acceptor")
			return
		default:
			conn, err := c.listener.Accept()
			if err != nil {
				if c.IsRunning() {
					logger.ErrorCF("maixcam", "Failed to accept connection", map[string]any{
						"error": err.Error(),
					})
				}
				return
			}

			c.clientsMux.Lock()
			if len(c.clients) >= maxConnections {
				c.clientsMux.Unlock()
				logger.WarnCF("maixcam", "Too many MaixCam connections, refusing a new one", map[string]any{
					"remote_addr": conn.RemoteAddr().String(),
				})
				conn.Close()
				continue
			}
			c.clients[conn] = deviceChatID(conn.RemoteAddr())
			c.clientsMux.Unlock()

			logger.InfoCF("maixcam", "New connection from MaixCam device", map[string]any{
				"remote_addr": conn.RemoteAddr().String(),
			})

			go c.handleConnection(conn)
		}
	}
}

// deviceChatID is the chat of a device: its address, so that each device
// gets its own replies, also after it reconnects.
func deviceChatID(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// authorized reports whether a message carries the configured token. Without
// a token every message is accepted.
func (c *MaixCamChannel) authorized(token string) bool {
	want := c.config.Token.String()
	if want == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1
}

// messageReader stops a device from sending one endless JSON value: the
// decoder may read at most limit bytes for each message.
type messageReader struct {
	r         io.Reader
	remaining int
}

func (m *messageReader) reset(limit int) { m.remaining = limit }

func (m *messageReader) Read(p []byte) (int, error) {
	if m.remaining <= 0 {
		return 0, errMessageTooLarge
	}
	if len(p) > m.remaining {
		p = p[:m.remaining]
	}
	n, err := m.r.Read(p)
	m.remaining -= n
	return n, err
}

func (c *MaixCamChannel) handleConnection(conn net.Conn) {
	logger.DebugC("maixcam", "Handling MaixCam connection")

	defer func() {
		conn.Close()
		c.clientsMux.Lock()
		delete(c.clients, conn)
		c.clientsMux.Unlock()
		logger.DebugC("maixcam", "Connection closed")
	}()
	defer channels.RecoverPanic("maixcam", "connection")

	chatID := deviceChatID(conn.RemoteAddr())
	reader := &messageReader{r: conn}
	decoder := json.NewDecoder(reader)

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(idleTimeout))
		reader.reset(maxMessageBytes)

		var msg MaixCamMessage
		if err := decoder.Decode(&msg); err != nil {
			if !errors.Is(err, io.EOF) {
				logger.WarnCF("maixcam", "Closing MaixCam connection after a bad message", map[string]any{
					"remote_addr": conn.RemoteAddr().String(),
					"error":       err.Error(),
				})
			}
			return
		}

		if !c.authorized(msg.Token) {
			logger.WarnCF("maixcam", "Closing MaixCam connection: wrong or missing token", map[string]any{
				"remote_addr": conn.RemoteAddr().String(),
			})
			return
		}

		c.processMessage(msg, chatID)
	}
}

func (c *MaixCamChannel) processMessage(msg MaixCamMessage, chatID string) {
	switch msg.Type {
	case "person_detected":
		c.handlePersonDetection(msg, chatID)
	case "heartbeat":
		logger.DebugC("maixcam", "Received heartbeat")
	case "status":
		c.handleStatusUpdate(msg)
	default:
		logger.WarnCF("maixcam", "Unknown message type", map[string]any{
			"type": msg.Type,
		})
	}
}

func (c *MaixCamChannel) handlePersonDetection(msg MaixCamMessage, chatID string) {
	logger.InfoCF("maixcam", "", map[string]any{
		"timestamp": msg.Timestamp,
		"data":      msg.Data,
	})

	senderID := "maixcam"

	classInfo, ok := msg.Data["class_name"].(string)
	if !ok {
		classInfo = "person"
	}

	score, _ := msg.Data["score"].(float64)
	x, _ := msg.Data["x"].(float64)
	y, _ := msg.Data["y"].(float64)
	w, _ := msg.Data["w"].(float64)
	h, _ := msg.Data["h"].(float64)

	content := fmt.Sprintf("📷 Person detected!\nClass: %s\nConfidence: %.2f%%\nPosition: (%.0f, %.0f)\nSize: %.0fx%.0f",
		classInfo, score*100, x, y, w, h)

	metadata := map[string]string{
		"timestamp": fmt.Sprintf("%.0f", msg.Timestamp),
		"class_id":  fmt.Sprintf("%.0f", msg.Data["class_id"]),
		"score":     fmt.Sprintf("%.2f", score),
		"x":         fmt.Sprintf("%.0f", x),
		"y":         fmt.Sprintf("%.0f", y),
		"w":         fmt.Sprintf("%.0f", w),
		"h":         fmt.Sprintf("%.0f", h),
	}

	sender := bus.SenderInfo{
		Platform:    "maixcam",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("maixcam", senderID),
	}

	inboundCtx := bus.InboundContext{
		Channel:  c.Name(),
		ChatID:   chatID,
		ChatType: "direct",
		SenderID: senderID,
		Raw:      metadata,
	}

	// There is nothing to download first: the access policy decides in
	// HandleInboundContext, which also records an unpaired device for the
	// owner to approve.
	c.HandleInboundContext(c.ctx, chatID, content, nil, inboundCtx, sender)
}

func (c *MaixCamChannel) handleStatusUpdate(msg MaixCamMessage) {
	logger.InfoCF("maixcam", "Status update from MaixCam", map[string]any{
		"status": msg.Data,
	})
}

func (c *MaixCamChannel) Stop(ctx context.Context) error {
	logger.InfoC("maixcam", "Stopping MaixCam channel")
	c.SetRunning(false)

	// Cancel context first to signal goroutines to exit
	if c.cancel != nil {
		c.cancel()
	}

	if c.listener != nil {
		c.listener.Close()
	}

	c.clientsMux.Lock()
	defer c.clientsMux.Unlock()

	for conn := range c.clients {
		conn.Close()
	}
	c.clients = make(map[net.Conn]string)

	logger.InfoC("maixcam", "MaixCam channel stopped")
	return nil
}

// connectionsFor returns the connections of the device a reply is for. A
// reply without a chat ID goes to the only connected device, if there is
// just one.
func (c *MaixCamChannel) connectionsFor(chatID string) []net.Conn {
	c.clientsMux.RLock()
	defer c.clientsMux.RUnlock()

	var conns []net.Conn
	for conn, id := range c.clients {
		if id == chatID {
			conns = append(conns, conn)
		}
	}
	if len(conns) == 0 && chatID == "" && len(c.clients) == 1 {
		for conn := range c.clients {
			conns = append(conns, conn)
		}
	}
	return conns
}

// Send delivers a reply to the device that asked, never to the others.
func (c *MaixCamChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	// Check ctx before entering write path
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	conns := c.connectionsFor(msg.ChatID)
	if len(conns) == 0 {
		logger.WarnCF("maixcam", "MaixCam device is not connected", map[string]any{"chat_id": msg.ChatID})
		return nil, fmt.Errorf("maixcam device %q is not connected: %w", msg.ChatID, channels.ErrSendFailed)
	}

	response := map[string]any{
		"type":      "command",
		"timestamp": float64(0),
		"message":   msg.Content,
		"chat_id":   msg.ChatID,
	}

	data, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal response: %w", err)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var sendErr error
	for _, conn := range conns {
		_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := conn.Write(data); err != nil {
			logger.ErrorCF("maixcam", "Failed to send to client", map[string]any{
				"client": conn.RemoteAddr().String(),
				"error":  err.Error(),
			})
			sendErr = fmt.Errorf("maixcam send: %w", channels.ErrTemporary)
		}
		_ = conn.SetWriteDeadline(time.Time{})
	}

	return nil, sendErr
}
