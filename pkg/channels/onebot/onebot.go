//go:build paused_channels

package onebot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/channels"
	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/identity"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/media"
	"github.com/xibodev/compa/v4/pkg/utils"
)

const (
	// wsReadLimit bounds one event from the OneBot implementation.
	wsReadLimit        = 4 << 20
	wsHandshakeTimeout = 10 * time.Second
	mediaTimeout       = 60 * time.Second
)

type OneBotChannel struct {
	*channels.BaseChannel
	config        *config.OneBotSettings
	downloadFn    func(urlStr, filename string) string
	maxMediaBytes int64
	conn          *websocket.Conn
	ctx           context.Context
	cancel        context.CancelFunc
	dedup         map[string]struct{}
	dedupRing     []string
	dedupIdx      int
	mu            sync.Mutex
	writeMu       sync.Mutex
	echoCounter   int64
	selfID        int64
	pending       map[string]chan json.RawMessage
	pendingMu     sync.Mutex
	lastMessageID sync.Map
}

type oneBotRawEvent struct {
	PostType      string          `json:"post_type"`
	MessageType   string          `json:"message_type"`
	SubType       string          `json:"sub_type"`
	MessageID     json.RawMessage `json:"message_id"`
	UserID        json.RawMessage `json:"user_id"`
	GroupID       json.RawMessage `json:"group_id"`
	RawMessage    string          `json:"raw_message"`
	Message       json.RawMessage `json:"message"`
	Sender        json.RawMessage `json:"sender"`
	SelfID        json.RawMessage `json:"self_id"`
	Time          json.RawMessage `json:"time"`
	MetaEventType string          `json:"meta_event_type"`
	NoticeType    string          `json:"notice_type"`
	Echo          string          `json:"echo"`
	RetCode       json.RawMessage `json:"retcode"`
	Status        json.RawMessage `json:"status"`
	Data          json.RawMessage `json:"data"`
}

type BotStatus struct {
	Online bool `json:"online"`
	Good   bool `json:"good"`
}

func isAPIResponse(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s == "ok" || s == "failed"
	}
	var bs BotStatus
	if json.Unmarshal(raw, &bs) == nil {
		return bs.Online || bs.Good
	}
	return false
}

type oneBotSender struct {
	UserID   json.RawMessage `json:"user_id"`
	Nickname string          `json:"nickname"`
	Card     string          `json:"card"`
}

type oneBotAPIRequest struct {
	Action string `json:"action"`
	Params any    `json:"params"`
	Echo   string `json:"echo,omitempty"`
}

type oneBotMessageSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

func NewOneBotChannel(
	bc *config.Channel,
	cfg *config.OneBotSettings,
	messageBus *bus.MessageBus,
) (*OneBotChannel, error) {
	base := channels.NewBaseChannel("onebot", cfg, messageBus, bc.AllowFrom,
		channels.WithGroupTrigger(bc.GroupTrigger),
		channels.WithReasoningChannelID(bc.ReasoningChannelID),
	)

	const dedupSize = 1024
	return &OneBotChannel{
		BaseChannel:   base,
		config:        cfg,
		maxMediaBytes: config.DefaultMaxMediaSize,
		dedup:         make(map[string]struct{}, dedupSize),
		dedupRing:     make([]string, dedupSize),
		dedupIdx:      0,
		pending:       make(map[string]chan json.RawMessage),
	}, nil
}

func (c *OneBotChannel) setMsgEmojiLike(messageID string, emojiID int, set bool) {
	go func() {
		_, err := c.sendAPIRequest("set_msg_emoji_like", map[string]any{
			"message_id": messageID,
			"emoji_id":   emojiID,
			"set":        set,
		}, 5*time.Second)
		if err != nil {
			logger.DebugCF("onebot", "Failed to set emoji like", map[string]any{
				"message_id": messageID,
				"error":      err.Error(),
			})
		}
	}()
}

// ReactToMessage implements channels.ReactionCapable.
// It adds an emoji reaction (ID 289) to group messages and returns an undo function.
// Private messages return a no-op since reactions are only meaningful in groups.
func (c *OneBotChannel) ReactToMessage(ctx context.Context, chatID, messageID string) (func(), error) {
	// Only react in group chats
	if !strings.HasPrefix(chatID, "group:") {
		return func() {}, nil
	}

	c.setMsgEmojiLike(messageID, 289, true)

	return func() {
		c.setMsgEmojiLike(messageID, 289, false)
	}, nil
}

func (c *OneBotChannel) Start(ctx context.Context) error {
	if c.config.WSUrl == "" {
		return fmt.Errorf("OneBot ws_url not configured")
	}

	logger.InfoCF("onebot", "Starting OneBot channel", map[string]any{
		"ws_url": c.config.WSUrl,
	})

	if err := channels.CheckWebSocketURL(ctx, c.config.WSUrl, "OneBot ws_url"); err != nil {
		return err
	}

	c.ctx, c.cancel = context.WithCancel(ctx)

	if err := c.connect(); err != nil {
		logger.WarnCF("onebot", "Initial connection failed, will retry in background", map[string]any{
			"error": err.Error(),
		})
	} else {
		go c.listen()
		c.fetchSelfID()
	}

	if c.config.ReconnectInterval > 0 {
		go c.reconnectLoop()
	} else {
		if c.conn == nil {
			return fmt.Errorf("failed to connect to OneBot and reconnect is disabled")
		}
	}

	c.SetRunning(true)
	logger.InfoC("onebot", "OneBot channel started successfully")

	return nil
}

func (c *OneBotChannel) connect() error {
	// A dialer of its own: changing the package-level websocket.DefaultDialer
	// would change every other user's too.
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: wsHandshakeTimeout,
	}

	header := make(map[string][]string)
	if c.config.AccessToken.String() != "" {
		header["Authorization"] = []string{"Bearer " + c.config.AccessToken.String()}
	}

	conn, resp, err := dialer.Dial(c.config.WSUrl, header)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return err
	}
	conn.SetReadLimit(wsReadLimit)

	conn.SetPongHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	go c.pinger(conn)

	logger.InfoC("onebot", "WebSocket connected")
	return nil
}

func (c *OneBotChannel) pinger(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.writeMu.Lock()
			err := conn.WriteMessage(websocket.PingMessage, nil)
			c.writeMu.Unlock()
			if err != nil {
				logger.DebugCF("onebot", "Ping write failed, stopping pinger", map[string]any{
					"error": err.Error(),
				})
				return
			}
		}
	}
}

func (c *OneBotChannel) fetchSelfID() {
	resp, err := c.sendAPIRequest("get_login_info", nil, 5*time.Second)
	if err != nil {
		logger.WarnCF("onebot", "Failed to get_login_info", map[string]any{
			"error": err.Error(),
		})
		return
	}

	type loginInfo struct {
		UserID   json.RawMessage `json:"user_id"`
		Nickname string          `json:"nickname"`
	}
	for _, extract := range []func() (*loginInfo, error){
		func() (*loginInfo, error) {
			var w struct {
				Data loginInfo `json:"data"`
			}
			err := json.Unmarshal(resp, &w)
			return &w.Data, err
		},
		func() (*loginInfo, error) {
			var f loginInfo
			err := json.Unmarshal(resp, &f)
			return &f, err
		},
	} {
		info, err := extract()
		if err != nil || len(info.UserID) == 0 {
			continue
		}
		if uid, err := parseJSONInt64(info.UserID); err == nil && uid > 0 {
			atomic.StoreInt64(&c.selfID, uid)
			logger.InfoCF("onebot", "Bot self ID retrieved", map[string]any{
				"self_id":  uid,
				"nickname": info.Nickname,
			})
			return
		}
	}

	logger.WarnCF("onebot", "Could not parse self ID from get_login_info response", map[string]any{
		"response": string(resp),
	})
}

func (c *OneBotChannel) sendAPIRequest(action string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("WebSocket not connected")
	}

	echo := fmt.Sprintf("api_%d_%d", time.Now().UnixNano(), atomic.AddInt64(&c.echoCounter, 1))

	ch := make(chan json.RawMessage, 1)
	c.pendingMu.Lock()
	c.pending[echo] = ch
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, echo)
		c.pendingMu.Unlock()
	}()

	req := oneBotAPIRequest{
		Action: action,
		Params: params,
		Echo:   echo,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal API request: %w", err)
	}

	c.writeMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = conn.WriteMessage(websocket.TextMessage, data)
	_ = conn.SetWriteDeadline(time.Time{})
	c.writeMu.Unlock()

	if err != nil {
		return nil, fmt.Errorf("failed to write API request: %w", err)
	}

	select {
	case resp := <-ch:
		if resp == nil {
			return nil, fmt.Errorf("API request %s: channel stopped", action)
		}
		return resp, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("API request %s timed out after %v", action, timeout)
	case <-c.ctx.Done():
		return nil, fmt.Errorf("context canceled")
	}
}

func (c *OneBotChannel) reconnectLoop() {
	interval := max(time.Duration(c.config.ReconnectInterval)*time.Second, 5*time.Second)

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(interval):
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()

			if conn == nil {
				logger.InfoC("onebot", "Attempting to reconnect...")
				if err := c.connect(); err != nil {
					logger.ErrorCF("onebot", "Reconnect failed", map[string]any{
						"error": err.Error(),
					})
				} else {
					go c.listen()
					c.fetchSelfID()
				}
			}
		}
	}
}

func (c *OneBotChannel) Stop(ctx context.Context) error {
	logger.InfoC("onebot", "Stopping OneBot channel")
	c.SetRunning(false)

	if c.cancel != nil {
		c.cancel()
	}

	c.pendingMu.Lock()
	for echo, ch := range c.pending {
		select {
		case ch <- nil: // non-blocking wake for blocked sendAPIRequest goroutines
		default:
		}
		delete(c.pending, echo)
	}
	c.pendingMu.Unlock()

	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	return nil
}

func (c *OneBotChannel) Send(ctx context.Context, msg bus.OutboundMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	// Check ctx before entering write path
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("OneBot WebSocket not connected")
	}

	action, params, err := c.buildSendRequest(msg)
	if err != nil {
		return nil, err
	}

	echo := fmt.Sprintf("send_%d", atomic.AddInt64(&c.echoCounter, 1))

	req := oneBotAPIRequest{
		Action: action,
		Params: params,
		Echo:   echo,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OneBot request: %w", err)
	}

	c.writeMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = conn.WriteMessage(websocket.TextMessage, data)
	_ = conn.SetWriteDeadline(time.Time{})
	c.writeMu.Unlock()

	if err != nil {
		logger.ErrorCF("onebot", "Failed to send message", map[string]any{
			"error": err.Error(),
		})
		return nil, fmt.Errorf("onebot send: %w", channels.ErrTemporary)
	}

	return nil, nil
}

// SendMedia implements the channels.MediaSender interface.
func (c *OneBotChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) ([]string, error) {
	if !c.IsRunning() {
		return nil, channels.ErrNotRunning
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("OneBot WebSocket not connected")
	}

	store := c.GetMediaStore()
	if store == nil {
		return nil, fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	// Build media segments
	var segments []oneBotMessageSegment
	for _, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("onebot", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		var segType string
		switch part.Type {
		case "image":
			segType = "image"
		case "video":
			segType = "video"
		case "audio":
			segType = "record"
		default:
			segType = "file"
		}

		segments = append(segments, oneBotMessageSegment{
			Type: segType,
			Data: map[string]any{"file": "file://" + localPath},
		})

		if part.Caption != "" {
			segments = append(segments, oneBotMessageSegment{
				Type: "text",
				Data: map[string]any{"text": part.Caption},
			})
		}
	}

	if len(segments) == 0 {
		return nil, nil
	}

	chatID := msg.ChatID
	var action, idKey string
	var rawID string
	if rest, ok := strings.CutPrefix(chatID, "group:"); ok {
		action, idKey, rawID = "send_group_msg", "group_id", rest
	} else if rest, ok := strings.CutPrefix(chatID, "private:"); ok {
		action, idKey, rawID = "send_private_msg", "user_id", rest
	} else {
		action, idKey, rawID = "send_private_msg", "user_id", chatID
	}

	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid %s in chatID: %s: %w", idKey, chatID, channels.ErrSendFailed)
	}

	echo := fmt.Sprintf("send_%d", atomic.AddInt64(&c.echoCounter, 1))

	req := oneBotAPIRequest{
		Action: action,
		Params: map[string]any{idKey: id, "message": segments},
		Echo:   echo,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal OneBot request: %w", err)
	}

	c.writeMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	err = conn.WriteMessage(websocket.TextMessage, data)
	_ = conn.SetWriteDeadline(time.Time{})
	c.writeMu.Unlock()

	if err != nil {
		logger.ErrorCF("onebot", "Failed to send media message", map[string]any{
			"error": err.Error(),
		})
		return nil, fmt.Errorf("onebot send media: %w", channels.ErrTemporary)
	}

	return nil, nil
}

func (c *OneBotChannel) buildMessageSegments(chatID, content string) []oneBotMessageSegment {
	var segments []oneBotMessageSegment

	if lastMsgID, ok := c.lastMessageID.Load(chatID); ok {
		if msgID, ok := lastMsgID.(string); ok && msgID != "" {
			segments = append(segments, oneBotMessageSegment{
				Type: "reply",
				Data: map[string]any{"id": msgID},
			})
		}
	}

	segments = append(segments, oneBotMessageSegment{
		Type: "text",
		Data: map[string]any{"text": content},
	})

	return segments
}

func (c *OneBotChannel) buildSendRequest(msg bus.OutboundMessage) (string, any, error) {
	chatID := msg.ChatID
	segments := c.buildMessageSegments(chatID, msg.Content)

	var action, idKey string
	var rawID string
	if rest, ok := strings.CutPrefix(chatID, "group:"); ok {
		action, idKey, rawID = "send_group_msg", "group_id", rest
	} else if rest, ok := strings.CutPrefix(chatID, "private:"); ok {
		action, idKey, rawID = "send_private_msg", "user_id", rest
	} else {
		action, idKey, rawID = "send_private_msg", "user_id", chatID
	}

	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil {
		return "", nil, fmt.Errorf("invalid %s in chatID: %s", idKey, chatID)
	}
	return action, map[string]any{idKey: id, "message": segments}, nil
}

func (c *OneBotChannel) listen() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		logger.WarnC("onebot", "WebSocket connection is nil, listener exiting")
		return
	}

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				logger.ErrorCF("onebot", "WebSocket read error", map[string]any{
					"error": err.Error(),
				})
				c.mu.Lock()
				if c.conn == conn {
					c.conn.Close()
					c.conn = nil
				}
				c.mu.Unlock()
				return
			}

			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))

			var raw oneBotRawEvent
			if err := json.Unmarshal(message, &raw); err != nil {
				logger.WarnCF("onebot", "Failed to unmarshal raw event", map[string]any{
					"error":   err.Error(),
					"payload": string(message),
				})
				continue
			}

			logger.DebugCF("onebot", "WebSocket event", map[string]any{
				"length":    len(message),
				"post_type": raw.PostType,
				"sub_type":  raw.SubType,
			})

			if raw.Echo != "" {
				c.pendingMu.Lock()
				ch, ok := c.pending[raw.Echo]
				c.pendingMu.Unlock()

				if ok {
					select {
					case ch <- message:
					default:
					}
				} else {
					logger.DebugCF("onebot", "Received API response (no waiter)", map[string]any{
						"echo":   raw.Echo,
						"status": string(raw.Status),
					})
				}
				continue
			}

			if isAPIResponse(raw.Status) {
				logger.DebugCF("onebot", "Received API response without echo, skipping", map[string]any{
					"status": string(raw.Status),
				})
				continue
			}

			c.handleRawEvent(&raw)
		}
	}
}

func parseJSONInt64(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, nil
	}

	var n int64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n, nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strconv.ParseInt(s, 10, 64)
	}
	return 0, fmt.Errorf("cannot parse as int64: %s", string(raw))
}

func parseJSONString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}

	return string(raw)
}

type parseMessageResult struct {
	Text           string
	IsBotMentioned bool
	Media          []string
	ReplyTo        string
	// pending lists the media of the message; it is downloaded only once the
	// message is admitted (see fetchMedia).
	pending []pendingMedia
}

// pendingMedia is a media segment not downloaded yet.
type pendingMedia struct {
	tag      string // how the text marks it, such as "[image]"
	url      string
	filename string
	at       int // where the segment stands in the message's text, in bytes
}

// parseMessageSegments parses a message and downloads its media.
func (c *OneBotChannel) parseMessageSegments(
	raw json.RawMessage,
	selfID int64,
	store media.MediaStore,
	scope string,
) parseMessageResult {
	result := parseSegments(raw, selfID)
	refs, fetched := c.fetchMedia(result.pending, store, scope)
	result.Media = refs
	result.Text = withMediaTags(result.Text, 0, fetched)
	return result
}

// cqUnescaper undoes OneBot's escaping of text and CQ code values.
var cqUnescaper = strings.NewReplacer("&#91;", "[", "&#93;", "]", "&#44;", ",", "&amp;", "&")

// cqSegments splits a message in OneBot's string format, text with CQ codes
// such as [CQ:image,file=a.png,url=https://...], into the segments of the
// array format: {"type": "image", "data": {"file": "a.png", "url": "..."}}.
func cqSegments(s string) []map[string]any {
	var segments []map[string]any
	addText := func(text string) {
		if text != "" {
			segments = append(segments, map[string]any{
				"type": "text", "data": map[string]any{"text": cqUnescaper.Replace(text)},
			})
		}
	}
	for {
		start := strings.Index(s, "[CQ:")
		end := strings.IndexByte(s[max(start, 0):], ']')
		if start < 0 || end < 0 {
			break
		}
		end += start
		addText(s[:start])
		fields := strings.Split(s[start+len("[CQ:"):end], ",")
		data := map[string]any{}
		for _, field := range fields[1:] {
			if key, value, ok := strings.Cut(field, "="); ok {
				data[key] = cqUnescaper.Replace(value)
			}
		}
		segments = append(segments, map[string]any{"type": fields[0], "data": data})
		s = s[end+1:]
	}
	addText(s)
	return segments
}

// parseSegments reads a message's text, mention, reply and the media it lists,
// without downloading anything.
func parseSegments(raw json.RawMessage, selfID int64) parseMessageResult {
	if len(raw) == 0 {
		return parseMessageResult{}
	}

	var segments []map[string]any
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		segments = cqSegments(s)
	} else if err := json.Unmarshal(raw, &segments); err != nil {
		return parseMessageResult{}
	}

	var text strings.Builder
	mentioned := false
	selfIDStr := strconv.FormatInt(selfID, 10)
	var pending []pendingMedia
	var replyTo string

	for _, seg := range segments {
		segType, _ := seg["type"].(string)
		data, _ := seg["data"].(map[string]any)

		switch segType {
		case "text":
			if data != nil {
				if t, ok := data["text"].(string); ok {
					text.WriteString(t)
				}
			}

		case "at":
			// "@all" addresses everyone in the group, not the bot.
			if data != nil && selfID > 0 && fmt.Sprintf("%v", data["qq"]) == selfIDStr {
				mentioned = true
			}

		case "image", "video", "file":
			if data != nil {
				url, _ := data["url"].(string)
				if url != "" {
					defaults := map[string]string{"image": "image.jpg", "video": "video.mp4", "file": "file"}
					filename := defaults[segType]
					if f, ok := data["file"].(string); ok && f != "" {
						filename = f
					} else if n, ok := data["name"].(string); ok && n != "" {
						filename = n
					}
					pending = append(pending, pendingMedia{
						tag: "[" + segType + "]", url: url, filename: filename, at: text.Len(),
					})
				}
			}

		case "record":
			if data != nil {
				url, _ := data["url"].(string)
				if url != "" {
					pending = append(pending, pendingMedia{tag: "[voice]", url: url, filename: "voice.amr", at: text.Len()})
				}
			}

		case "reply":
			if data != nil {
				if id, ok := data["id"]; ok {
					replyTo = fmt.Sprintf("%v", id)
				}
			}

		case "face":
			if data != nil {
				faceID, _ := data["id"]
				fmt.Fprintf(&text, "[face:%v]", faceID)
			}

		case "forward":
			text.WriteString("[forward message]")

		default:
		}
	}

	// Where each media segment stands, in the trimmed text.
	joined := text.String()
	lead := len(joined) - len(strings.TrimLeftFunc(joined, unicode.IsSpace))
	trimmed := strings.TrimSpace(joined)
	for i := range pending {
		pending[i].at = min(max(pending[i].at-lead, 0), len(trimmed))
	}

	return parseMessageResult{
		Text:           trimmed,
		IsBotMentioned: mentioned,
		ReplyTo:        replyTo,
		pending:        pending,
	}
}

// fetchMedia downloads the listed media into the store and returns their refs
// and the items it downloaded, whose tags stand for them in the text.
func (c *OneBotChannel) fetchMedia(
	pending []pendingMedia,
	store media.MediaStore,
	scope string,
) ([]string, []pendingMedia) {
	var refs []string
	var fetched []pendingMedia
	for _, item := range pending {
		localPath := c.downloadInboundFile(item.url, item.filename)
		if localPath == "" {
			continue
		}
		ref := localPath // fallback when there is no store
		if store != nil {
			if stored, err := store.Store(localPath, media.MediaMeta{
				Filename:      item.filename,
				Source:        "onebot",
				CleanupPolicy: media.CleanupPolicyDeleteOnCleanup,
			}, scope); err == nil {
				ref = stored
			}
		}
		refs = append(refs, ref)
		fetched = append(fetched, item)
	}
	return refs, fetched
}

// withMediaTags puts the tag of each fetched media item into text where its
// segment stood, so the agent reads each picture between the right words.
// text is the message's text from byte base on, as the group trigger left it.
func withMediaTags(text string, base int, fetched []pendingMedia) string {
	var b strings.Builder
	pos := 0
	for _, item := range fetched {
		at := min(max(item.at-base, pos), len(text))
		b.WriteString(text[pos:at])
		b.WriteString(item.tag)
		pos = at
	}
	b.WriteString(text[pos:])
	return strings.TrimSpace(b.String())
}

// downloadInboundFile downloads a media URL into the media directory,
// refusing private and local network hosts and files over the size limit.
func (c *OneBotChannel) downloadInboundFile(urlStr, filename string) string {
	if c.downloadFn != nil {
		return c.downloadFn(urlStr, filename)
	}

	if err := utils.ValidateSafeHTTPURL(urlStr, nil, nil); err != nil {
		logger.WarnCF("onebot", "Blocked unsafe media URL", map[string]any{"error": err.Error()})
		return ""
	}
	client, err := utils.CreateSafeHTTPClient(utils.SafeHTTPClientOptions{Timeout: mediaTimeout})
	if err != nil {
		logger.ErrorCF("onebot", "Failed to create media download client", map[string]any{"error": err.Error()})
		return ""
	}
	req, err := http.NewRequest(http.MethodGet, urlStr, nil)
	if err != nil {
		return ""
	}
	utils.AllowConfiguredProxyFirstHop(req, client.Transport)

	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	maxBytes := c.maxMediaBytes
	if maxBytes <= 0 {
		maxBytes = config.DefaultMaxMediaSize
	}
	tmpPath, err := utils.DownloadToFile(ctx, client, req, maxBytes)
	if err != nil {
		logger.WarnCF("onebot", "Failed to download media", map[string]any{
			"filename": filename,
			"error":    err.Error(),
		})
		return ""
	}
	return moveToMediaDir(tmpPath, filename)
}

// moveToMediaDir moves a downloaded file into the media directory under its
// file name, whose extension tells later steps what the file is.
func moveToMediaDir(tmpPath, filename string) string {
	if err := os.MkdirAll(media.TempDir(), 0o700); err != nil {
		return tmpPath
	}
	dst := filepath.Join(media.TempDir(), uuid.New().String()[:8]+"_"+utils.SanitizeFilename(filename))
	if err := os.Rename(tmpPath, dst); err != nil {
		return tmpPath
	}
	return dst
}

func (c *OneBotChannel) handleRawEvent(raw *oneBotRawEvent) {
	defer channels.RecoverPanic(c.Name(), "event")

	switch raw.PostType {
	case "message":
		// handleMessage applies the access policy, before it downloads
		// anything.
		c.handleMessage(raw)

	case "message_sent":
		logger.DebugCF("onebot", "Bot sent message event", map[string]any{
			"message_type": raw.MessageType,
			"message_id":   parseJSONString(raw.MessageID),
		})

	case "meta_event":
		c.handleMetaEvent(raw)

	case "notice":
		c.handleNoticeEvent(raw)

	case "request":
		logger.DebugCF("onebot", "Request event received", map[string]any{
			"sub_type": raw.SubType,
		})

	case "":
		logger.DebugCF("onebot", "Event with empty post_type (possibly API response)", map[string]any{
			"echo":   raw.Echo,
			"status": raw.Status,
		})

	default:
		logger.DebugCF("onebot", "Unknown post_type", map[string]any{
			"post_type": raw.PostType,
		})
	}
}

func (c *OneBotChannel) handleMetaEvent(raw *oneBotRawEvent) {
	if raw.MetaEventType == "lifecycle" {
		logger.InfoCF("onebot", "Lifecycle event", map[string]any{"sub_type": raw.SubType})
	} else if raw.MetaEventType != "heartbeat" {
		logger.DebugCF("onebot", "Meta event: "+raw.MetaEventType, nil)
	}
}

func (c *OneBotChannel) handleNoticeEvent(raw *oneBotRawEvent) {
	fields := map[string]any{
		"notice_type": raw.NoticeType,
		"sub_type":    raw.SubType,
		"group_id":    parseJSONString(raw.GroupID),
		"user_id":     parseJSONString(raw.UserID),
		"message_id":  parseJSONString(raw.MessageID),
	}
	switch raw.NoticeType {
	case "group_recall", "group_increase", "group_decrease",
		"friend_add", "group_admin", "group_ban":
		logger.InfoCF("onebot", "Notice: "+raw.NoticeType, fields)
	default:
		logger.DebugCF("onebot", "Notice: "+raw.NoticeType, fields)
	}
}

func (c *OneBotChannel) handleMessage(raw *oneBotRawEvent) {
	// Parse fields from raw event
	userID, err := parseJSONInt64(raw.UserID)
	if err != nil {
		logger.WarnCF("onebot", "Failed to parse user_id", map[string]any{
			"error": err.Error(),
			"raw":   string(raw.UserID),
		})
		return
	}

	groupID, _ := parseJSONInt64(raw.GroupID)
	selfID, _ := parseJSONInt64(raw.SelfID)
	messageID := parseJSONString(raw.MessageID)

	if selfID == 0 {
		selfID = atomic.LoadInt64(&c.selfID)
	}

	// Compute scope for media store before parsing (parsing may download files)
	var chatIDForScope string
	switch raw.MessageType {
	case "group":
		chatIDForScope = "group:" + strconv.FormatInt(groupID, 10)
	default:
		chatIDForScope = "private:" + strconv.FormatInt(userID, 10)
	}
	scope := channels.BuildMediaScope("onebot", chatIDForScope, messageID)

	// Media is downloaded only after the group trigger and the allowlist
	// admitted the message.
	parsed := parseSegments(raw.Message, selfID)
	hasMedia := len(parsed.pending) > 0
	isBotMentioned := parsed.IsBotMentioned

	content := raw.RawMessage
	if content == "" {
		content = parsed.Text
	} else if selfID > 0 {
		cqAt := fmt.Sprintf("[CQ:at,qq=%d]", selfID)
		if strings.Contains(content, cqAt) {
			isBotMentioned = true
			content = strings.ReplaceAll(content, cqAt, "")
			content = strings.TrimSpace(content)
		}
	}

	// The parsed text replaces raw CQ codes of media and replies; for media
	// the tags are added once it is downloaded.
	if content != parsed.Text && (hasMedia || (parsed.ReplyTo != "" && parsed.Text != "")) {
		content = parsed.Text
	}

	var sender oneBotSender
	if len(raw.Sender) > 0 {
		if err := json.Unmarshal(raw.Sender, &sender); err != nil {
			logger.WarnCF("onebot", "Failed to parse sender", map[string]any{
				"error":  err.Error(),
				"sender": string(raw.Sender),
			})
		}
	}

	if c.isDuplicate(messageID) {
		logger.DebugCF("onebot", "Duplicate message, skipping", map[string]any{
			"message_id": messageID,
		})
		return
	}

	if content == "" && !hasMedia {
		logger.DebugCF("onebot", "Received empty message, ignoring", map[string]any{
			"message_id": messageID,
		})
		return
	}

	senderID := strconv.FormatInt(userID, 10)
	var chatID string
	var contextChatType string

	metadata := map[string]string{}

	if parsed.ReplyTo != "" {
		metadata["reply_to_message_id"] = parsed.ReplyTo
	}

	switch raw.MessageType {
	case "private":
		chatID = "private:" + senderID
		contextChatType = "direct"

	case "group":
		groupIDStr := strconv.FormatInt(groupID, 10)
		chatID = "group:" + groupIDStr
		contextChatType = "group"
		metadata["group_id"] = groupIDStr

		senderUserID, _ := parseJSONInt64(sender.UserID)
		if senderUserID > 0 {
			metadata["sender_user_id"] = strconv.FormatInt(senderUserID, 10)
		}

		if sender.Card != "" {
			metadata["sender_name"] = sender.Card
		} else if sender.Nickname != "" {
			metadata["sender_name"] = sender.Nickname
		}

		respond, strippedContent := c.ShouldRespondInGroup(isBotMentioned, content)
		if !respond {
			logger.DebugCF("onebot", "Group message ignored (no trigger)", map[string]any{
				"sender":       senderID,
				"group":        groupIDStr,
				"is_mentioned": isBotMentioned,
				"content":      truncate(content, 100),
			})
			return
		}
		content = strippedContent

	default:
		logger.WarnCF("onebot", "Unknown message type, cannot route", map[string]any{
			"type":       raw.MessageType,
			"message_id": messageID,
			"user_id":    userID,
		})
		return
	}

	if sender.Nickname != "" {
		metadata["nickname"] = sender.Nickname
	}

	senderInfo := bus.SenderInfo{
		Platform:    "onebot",
		PlatformID:  senderID,
		CanonicalID: identity.BuildCanonicalID("onebot", senderID),
		DisplayName: sender.Nickname,
	}

	inboundCtx := bus.InboundContext{
		Channel:          c.Name(),
		ChatID:           chatID,
		ChatType:         contextChatType,
		SenderID:         senderID,
		MessageID:        messageID,
		Mentioned:        isBotMentioned,
		ReplyToMessageID: parsed.ReplyTo,
		Raw:              metadata,
	}

	// Decide on the message before downloading its media. A private message
	// the policy rejects still goes to it, as text only, so that an unpaired
	// sender is recorded for the owner to approve.
	if !c.Admits(contextChatType, senderInfo, chatID) {
		logger.DebugCF("onebot", "Message not admitted by the access policy", map[string]any{
			"sender": senderID,
		})
		if contextChatType == "direct" {
			if content == "" {
				content = "[media]"
			}
			c.HandleInboundContext(c.ctx, chatID, content, nil, inboundCtx, senderInfo)
		}
		return
	}

	if hasMedia {
		refs, fetched := c.fetchMedia(parsed.pending, c.GetMediaStore(), scope)
		parsed.Media = refs
		// content is parsed.Text, less any prefix the group trigger took off;
		// where that can't be told, the tags go at the end.
		base := -len(content)
		if strings.HasSuffix(parsed.Text, content) {
			base = len(parsed.Text) - len(content)
		}
		content = withMediaTags(content, base, fetched)
		if content == "" {
			return
		}
	}

	logger.InfoCF("onebot", "Received "+raw.MessageType+" message", map[string]any{
		"sender":      senderID,
		"chat_id":     chatID,
		"message_id":  messageID,
		"length":      len(content),
		"content":     truncate(content, 100),
		"media_count": len(parsed.Media),
	})

	c.lastMessageID.Store(chatID, messageID)

	c.HandleInboundContext(c.ctx, chatID, content, parsed.Media, inboundCtx, senderInfo)
}

func (c *OneBotChannel) isDuplicate(messageID string) bool {
	if messageID == "" || messageID == "0" {
		return false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.dedup[messageID]; exists {
		return true
	}

	if old := c.dedupRing[c.dedupIdx]; old != "" {
		delete(c.dedup, old)
	}
	c.dedupRing[c.dedupIdx] = messageID
	c.dedup[messageID] = struct{}{}
	c.dedupIdx = (c.dedupIdx + 1) % len(c.dedupRing)

	return false
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}

// VoiceCapabilities returns the voice capabilities of the channel.
func (c *OneBotChannel) VoiceCapabilities() channels.VoiceCapabilities {
	return channels.VoiceCapabilities{ASR: true, TTS: true}
}
