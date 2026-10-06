package irc

import (
	"context"
	"crypto/tls"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ergochat/irc-go/ircevent"
	"github.com/ergochat/irc-go/ircmsg"
	"golang.org/x/time/rate"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/channels"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/logger"
)

// Servers disconnect clients that send lines in fast bursts ("Excess Flood"),
// so lines go out at sendInterval after a burst of sendBurst.
const (
	sendInterval = 700 * time.Millisecond
	sendBurst    = 4
)

// IRCChannel implements the Channel interface for IRC servers.
type IRCChannel struct {
	*channels.BaseChannel
	bc      *config.Channel
	config  *config.IRCSettings
	conn    *ircevent.Connection
	ctx     context.Context
	cancel  context.CancelFunc
	limiter *rate.Limiter
	// dmNicks maps a direct chat, named after the sender's account when
	// they are logged in, to the nick replies go to.
	dmNicks sync.Map
}

// NewIRCChannel creates a new IRC channel.
func NewIRCChannel(bc *config.Channel, cfg *config.IRCSettings, messageBus *bus.MessageBus) (*IRCChannel, error) {
	if cfg.Server == "" {
		return nil, fmt.Errorf("irc server is required")
	}
	if cfg.Nick == "" {
		return nil, fmt.Errorf("irc nick is required")
	}

	base := channels.NewBaseChannel("irc", cfg, messageBus, foldAllowList(bc.AllowFrom),
		channels.WithMaxMessageLength(400),
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	return &IRCChannel{
		BaseChannel: base,
		bc:          bc,
		config:      cfg,
		limiter:     rate.NewLimiter(rate.Every(sendInterval), sendBurst),
	}, nil
}

// Start connects to the IRC server and begins listening.
func (c *IRCChannel) Start(ctx context.Context) error {
	logger.InfoC("irc", "Starting IRC channel")
	c.ctx, c.cancel = context.WithCancel(ctx)

	user := c.config.User
	if user == "" {
		user = c.config.Nick
	}
	realName := c.config.RealName
	if realName == "" {
		realName = c.config.Nick
	}
	caps := []string(c.config.RequestCaps)
	if len(caps) == 0 {
		caps = []string{"server-time", "message-tags"}
	}
	// account-tag tells who is logged in to which account; senders are
	// identified by it when the server offers it.
	if !slices.Contains(caps, "account-tag") {
		caps = append(slices.Clone(caps), "account-tag")
	}

	conn := &ircevent.Connection{
		Server:      c.config.Server,
		Nick:        c.config.Nick,
		User:        user,
		RealName:    realName,
		Password:    c.config.Password.String(),
		UseTLS:      c.config.TLS,
		RequestCaps: caps,
		QuitMessage: "Goodbye",
		Debug:       false,
		Log:         nil,
	}

	if c.config.TLS {
		conn.TLSConfig = &tls.Config{
			ServerName: extractHost(c.config.Server),
		}
	}

	// SASL auth (takes priority over NickServ)
	if c.config.SASLUser != "" && c.config.SASLPassword.String() != "" {
		conn.SASLLogin = c.config.SASLUser
		conn.SASLPassword = c.config.SASLPassword.String()
	}

	// Register event handlers
	conn.AddConnectCallback(func(e ircmsg.Message) {
		defer channels.RecoverPanic(c.Name(), "connect")
		c.onConnect(conn)
	})
	conn.AddCallback("PRIVMSG", func(e ircmsg.Message) {
		defer channels.RecoverPanic(c.Name(), "privmsg")
		c.onPrivmsg(conn, e)
	})

	if err := conn.Connect(); err != nil {
		return fmt.Errorf("irc connect failed: %w", err)
	}

	c.conn = conn

	// ircevent.Connection.Loop() handles reconnection internally.
	go conn.Loop()

	c.SetRunning(true)
	logger.InfoCF("irc", "IRC channel started", map[string]any{
		"server": c.config.Server,
		"nick":   c.config.Nick,
	})
	return nil
}

// Stop disconnects from the IRC server.
func (c *IRCChannel) Stop(ctx context.Context) error {
	logger.InfoC("irc", "Stopping IRC channel")
	c.SetRunning(false)

	if c.conn != nil {
		c.conn.Quit()
	}
	if c.cancel != nil {
		c.cancel()
	}

	logger.InfoC("irc", "IRC channel stopped")
	return nil
}

// Send sends a message to an IRC channel or user, one PRIVMSG per line, each
// within the protocol's 512-byte line limit, at a pace servers accept.
func (c *IRCChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	target := msg.ChatID
	if target == "" {
		return nil, fmt.Errorf("chat ID is empty: %w", channels.ErrSendFailed)
	}
	if nick, ok := c.dmNicks.Load(target); ok {
		target = nick.(string)
	}

	if strings.TrimSpace(msg.Content) == "" {
		return nil, nil
	}

	user := c.config.User
	if user == "" {
		user = c.config.Nick
	}
	nick := c.conn.CurrentNick()
	if nick == "" {
		nick = c.config.Nick
	}
	lines := privmsgLines(msg.Content, privmsgTextBudget(nick, user, target))
	for _, line := range lines {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, err
		}
		if err := c.conn.Privmsg(target, line); err != nil {
			return nil, fmt.Errorf("irc send: %v: %w", err, channels.ErrTemporary)
		}
	}

	logger.DebugCF("irc", "Message sent", map[string]any{
		"target": target,
		"lines":  len(lines),
	})
	return nil, nil
}

// StartTyping implements channels.TypingCapable using IRCv3 +typing client tag.
// Requires typing.enabled in config and server support for message-tags capability.
func (c *IRCChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	noop := func() {}

	if !c.bc.Typing.Enabled || !c.IsRunning() || c.conn == nil {
		return noop, nil
	}

	// Check if server supports message-tags (required for TAGMSG)
	if _, ok := c.conn.AcknowledgedCaps()["message-tags"]; !ok {
		return noop, nil
	}

	c.conn.SendWithTags(map[string]string{"+typing": "active"}, "TAGMSG", chatID)

	return func() {
		if c.IsRunning() && c.conn != nil {
			c.conn.SendWithTags(map[string]string{"+typing": "done"}, "TAGMSG", chatID)
		}
	}, nil
}

// extractHost returns the hostname portion of a host:port string.
func extractHost(server string) string {
	host, _, found := strings.Cut(server, ":")
	if found {
		return host
	}
	return server
}
