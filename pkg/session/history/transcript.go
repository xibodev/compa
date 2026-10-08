// Package history reads the web chat's sessions from a workspace's session
// store: the list of sessions and each one's transcript, as the launcher's
// /api/sessions and the gateway's /web/sessions serve them.
package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/memory"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/providers/messageutil"
	"github.com/xibodev/compa/v3/pkg/session"
	"github.com/xibodev/compa/v3/pkg/utils"
)

// sessionFile is a session read from the JSONL store: its visible history plus
// the summary and timestamps kept in its metadata file.
type sessionFile struct {
	Key      string              `json:"key"`
	Messages []providers.Message `json:"messages"`
	Summary  string              `json:"summary,omitempty"`
	Created  time.Time           `json:"created"`
	Updated  time.Time           `json:"updated"`
}

// Summary is a lightweight summary returned by GET /api/sessions.
type Summary struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Preview      string `json:"preview"`
	MessageCount int    `json:"message_count"`
	Created      string `json:"created"`
	Updated      string `json:"updated"`
}

type Message struct {
	Role               string                  `json:"role"`
	Content            string                  `json:"content"`
	Kind               string                  `json:"kind,omitempty"`
	ModelName          string                  `json:"model_name,omitempty"`
	RequestedSelection string                  `json:"requested_selection,omitempty"`
	ServedTarget       string                  `json:"served_target,omitempty"`
	ServedIdentity     string                  `json:"served_identity,omitempty"`
	CreatedAt          *time.Time              `json:"created_at,omitempty"`
	Media              []string                `json:"media,omitempty"`
	Attachments        []Attachment            `json:"attachments,omitempty"`
	ToolCalls          []utils.VisibleToolCall `json:"tool_calls,omitempty"`
}

type Attachment struct {
	Type        string `json:"type,omitempty"`
	URL         string `json:"url,omitempty"`
	Filename    string `json:"filename,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

const (
	// Keep the session API aligned with the shared JSONL store reader limit in
	// pkg/memory/jsonl.go so oversized lines fail consistently everywhere.
	maxSessionJSONLLineSize = 10 * 1024 * 1024
	maxSessionTitleRunes    = 60

	handledToolResponseSummaryText = "Requested output delivered via tool attachment."
)

func defaultToolFeedbackMaxArgsLength() int {
	defaults := config.AgentDefaults{}
	return defaults.GetToolFeedbackMaxArgsLength()
}

// sanitizeSessionKey maps a session key onto its file name the same way the
// JSONL store in pkg/memory does.
func sanitizeSessionKey(key string) string {
	key = strings.ReplaceAll(key, ":", "_")
	key = strings.ReplaceAll(key, "/", "_")
	key = strings.ReplaceAll(key, "\\", "_")
	return key
}

func readSessionMeta(path, sessionKey string) (memory.SessionMeta, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return memory.SessionMeta{Key: sessionKey}, nil
	}
	if err != nil {
		return memory.SessionMeta{}, err
	}

	var meta memory.SessionMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return memory.SessionMeta{}, err
	}
	if meta.Key == "" {
		meta.Key = sessionKey
	}
	return meta, nil
}

// sessionHistoryRead, when a test sets it, observes every history file the
// session API reads.
var sessionHistoryRead func(path string)

func readSessionMessages(path string, skip int) ([]providers.Message, error) {
	if sessionHistoryRead != nil {
		sessionHistoryRead(path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	msgs := make([]providers.Message, 0)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSessionJSONLLineSize)

	seen := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		seen++
		if seen <= skip {
			continue
		}

		var msg providers.Message
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if messageutil.IsTransientAssistantThoughtMessage(msg) {
			continue
		}
		msgs = append(msgs, msg)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

func readJSONLSession(dir, sessionKey string) (sessionFile, error) {
	metaPath := filepath.Join(dir, sanitizeSessionKey(sessionKey)) + ".meta.json"
	meta, err := readSessionMeta(metaPath, sessionKey)
	if err != nil {
		return sessionFile{}, err
	}
	return readJSONLSessionWithMeta(dir, sessionKey, meta)
}

// readJSONLSessionWithMeta reads the history of a session whose metadata has
// already been read.
func readJSONLSessionWithMeta(dir, sessionKey string, meta memory.SessionMeta) (sessionFile, error) {
	jsonlPath := filepath.Join(dir, sanitizeSessionKey(sessionKey)) + ".jsonl"
	messages, err := readSessionMessages(jsonlPath, meta.Skip)
	if err != nil {
		return sessionFile{}, err
	}

	created, updated := meta.CreatedAt, meta.UpdatedAt
	if created.IsZero() || updated.IsZero() {
		if info, statErr := os.Stat(jsonlPath); statErr == nil {
			created, updated = sessionTimes(meta, info.ModTime())
		}
	}

	return sessionFile{
		Key:      meta.Key,
		Messages: messages,
		Summary:  meta.Summary,
		Created:  created,
		Updated:  updated,
	}, nil
}

// sessionTimes returns a session's created and updated times, taking the
// history file's modification time for any the metadata lacks.
func sessionTimes(meta memory.SessionMeta, modTime time.Time) (time.Time, time.Time) {
	created, updated := meta.CreatedAt, meta.UpdatedAt
	if created.IsZero() {
		created = modTime
	}
	if updated.IsZero() {
		updated = modTime
	}
	return created, updated
}

// webChatJSONLSessionRef pairs the web chat session ID shown to the web UI with the
// opaque session key its history is stored under.
type webChatJSONLSessionRef struct {
	ID  string
	Key string
	// meta is the metadata the session was found by.
	meta memory.SessionMeta
}

func extractWebChatSessionIDFromScope(scope session.SessionScope) (string, bool) {
	if !strings.EqualFold(strings.TrimSpace(scope.Channel), "web") {
		return "", false
	}

	candidates := []string{
		strings.TrimSpace(scope.Values["sender"]),
		strings.TrimSpace(scope.Values["chat"]),
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if idx := strings.Index(candidate, "web:"); idx >= 0 {
			sessionID := strings.TrimSpace(candidate[idx+len("web:"):])
			if sessionID != "" {
				return sessionID, true
			}
		}
	}
	return "", false
}

// sessionRefFromMeta identifies a web chat session by the scope the agent loop
// recorded in its metadata.
func sessionRefFromMeta(meta memory.SessionMeta) (webChatJSONLSessionRef, bool) {
	if len(meta.Scope) == 0 {
		return webChatJSONLSessionRef{}, false
	}
	var scope session.SessionScope
	if err := json.Unmarshal(meta.Scope, &scope); err != nil {
		return webChatJSONLSessionRef{}, false
	}
	sessionID, ok := extractWebChatSessionIDFromScope(scope)
	if !ok {
		return webChatJSONLSessionRef{}, false
	}
	return webChatJSONLSessionRef{ID: sessionID, Key: meta.Key, meta: meta}, true
}

func findWebChatJSONLSessions(dir string) ([]webChatJSONLSessionRef, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	refs := make([]webChatJSONLSessionRef, 0)
	index := make(map[string]int)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".meta.json") {
			continue
		}
		meta, err := readSessionMeta(filepath.Join(dir, entry.Name()), "")
		if err != nil {
			continue
		}
		ref, ok := sessionRefFromMeta(meta)
		if !ok || ref.Key == "" || ref.ID == "" {
			continue
		}
		// Two files can name the same session: the one updated last holds it.
		if i, exists := index[ref.ID]; exists {
			if ref.meta.UpdatedAt.After(refs[i].meta.UpdatedAt) {
				refs[i] = ref
			}
			continue
		}
		index[ref.ID] = len(refs)
		refs = append(refs, ref)
	}
	return refs, nil
}

func findWebChatJSONLSession(dir, sessionID string) (webChatJSONLSessionRef, error) {
	refs, err := findWebChatJSONLSessions(dir)
	if err != nil {
		return webChatJSONLSessionRef{}, err
	}
	for _, ref := range refs {
		// The scope keeps the id in lower case.
		if strings.EqualFold(ref.ID, sessionID) {
			return ref, nil
		}
	}
	return webChatJSONLSessionRef{}, os.ErrNotExist
}

func buildSessionListItem(sessionID string, sess sessionFile, toolFeedbackMaxArgsLength int) Summary {
	transcript := visibleSessionMessages(sess.Messages, toolFeedbackMaxArgsLength)

	preview := ""
	for _, msg := range transcript {
		if msg.Role == "user" {
			preview = sessionChatMessagePreview(msg)
		}
		if preview != "" {
			break
		}
	}
	preview = truncateRunes(preview, maxSessionTitleRunes)

	if preview == "" {
		preview = "(empty)"
	}
	title := preview

	return Summary{
		ID:           sessionID,
		Title:        title,
		Preview:      preview,
		MessageCount: len(transcript),
		Created:      sess.Created.Format(time.RFC3339),
		Updated:      sess.Updated.Format(time.RFC3339),
	}
}

func isEmptySession(sess sessionFile) bool {
	return len(sess.Messages) == 0 && strings.TrimSpace(sess.Summary) == ""
}

func truncateRunes(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= maxLen {
		return string(runes)
	}
	return string(runes[:maxLen]) + "..."
}

func sessionChatMessageVisible(msg Message) bool {
	return strings.TrimSpace(msg.Content) != "" ||
		len(msg.Media) > 0 ||
		len(msg.Attachments) > 0 ||
		len(msg.ToolCalls) > 0
}

func sessionChatMessagePreview(msg Message) string {
	if content := strings.TrimSpace(msg.Content); content != "" {
		return content
	}
	if len(msg.Attachments) > 0 {
		if strings.EqualFold(strings.TrimSpace(msg.Attachments[0].Type), "image") {
			return "[image]"
		}
		return "[attachment]"
	}
	if len(msg.Media) > 0 {
		if strings.HasPrefix(strings.TrimSpace(msg.Media[0]), "data:image/") {
			return "[image]"
		}
		return "[attachment]"
	}
	if len(msg.ToolCalls) > 0 {
		return "[tool call]"
	}
	return ""
}

func visibleSessionMessages(messages []providers.Message, toolFeedbackMaxArgsLength int) []Message {
	return sessionTranscriptMessages(messages, toolFeedbackMaxArgsLength, false)
}

func detailSessionMessages(messages []providers.Message, toolFeedbackMaxArgsLength int) []Message {
	return sessionTranscriptMessages(messages, toolFeedbackMaxArgsLength, true)
}

func sessionTranscriptMessages(
	messages []providers.Message,
	toolFeedbackMaxArgsLength int,
	includeThoughts bool,
) []Message {
	transcript := make([]Message, 0, len(messages))

	for _, msg := range messages {
		attachments := sessionAttachments(msg)

		switch msg.Role {
		case "tool":
			continue

		case "user":
			chatMsg := Message{
				Role:               "user",
				Content:            msg.Content,
				ModelName:          msg.ModelName,
				RequestedSelection: msg.RequestedSelection,
				CreatedAt:          msg.CreatedAt,
				Media:              append([]string(nil), msg.Media...),
				Attachments:        attachments,
			}
			if sessionChatMessageVisible(chatMsg) {
				transcript = append(transcript, chatMsg)
			}

		case "assistant":
			if messageutil.IsTransientAssistantThoughtMessage(msg) {
				continue
			}
			if includeThoughts {
				if thoughtMsg, ok := assistantThoughtMessage(msg); ok {
					transcript = append(transcript, thoughtMsg)
				}
			}

			toolCallsMsg, hasToolCallsMsg := assistantToolCallsMessage(
				msg.ToolCalls,
				msg.ModelName,
				toolFeedbackMaxArgsLength,
				msg.CreatedAt,
			)
			visibleToolMessages := visibleAssistantToolMessages(msg.ToolCalls, msg.ModelName, msg.CreatedAt)

			// Web chat can persist both visible `message` tool output and a
			// later plain assistant reply in the same turn. Hide only the fixed
			// internal summary that marks handled tool delivery.
			content := msg.Content
			if assistantMessageInternalOnly(msg) {
				if len(attachments) == 0 {
					if hasToolCallsMsg {
						transcript = append(transcript, toolCallsMsg)
					}
					if len(visibleToolMessages) > 0 {
						transcript = append(transcript, visibleToolMessages...)
					}
					continue
				}
				content = ""
			}
			if hasToolCallsMsg && utils.ToolCallExplanationDuplicatesContent(content, msg.ToolCalls) {
				content = ""
			}

			chatMsg := Message{
				Role:               "assistant",
				Content:            content,
				ModelName:          msg.ModelName,
				RequestedSelection: msg.RequestedSelection,
				ServedTarget:       msg.ServedTarget,
				ServedIdentity:     msg.ServedIdentity,
				CreatedAt:          msg.CreatedAt,
				Media:              append([]string(nil), msg.Media...),
				Attachments:        attachments,
			}
			if !sessionChatMessageVisible(chatMsg) {
				if hasToolCallsMsg {
					transcript = append(transcript, toolCallsMsg)
				}
				if len(visibleToolMessages) > 0 {
					transcript = append(transcript, visibleToolMessages...)
				}
				continue
			}

			transcript = append(transcript, chatMsg)
			if hasToolCallsMsg {
				transcript = append(transcript, toolCallsMsg)
			}
			if len(visibleToolMessages) > 0 {
				transcript = append(transcript, visibleToolMessages...)
			}
		}
	}

	return filterSessionChatMessages(transcript)
}

func filterSessionChatMessages(messages []Message) []Message {
	filtered := messages[:0]
	for _, msg := range messages {
		if msg.Role != "user" && msg.Role != "assistant" {
			continue
		}
		filtered = append(filtered, msg)
	}
	return filtered
}

func sessionAttachments(msg providers.Message) []Attachment {
	if len(msg.Attachments) == 0 {
		return nil
	}

	attachments := make([]Attachment, 0, len(msg.Attachments))
	for _, attachment := range msg.Attachments {
		urlValue, ok := sessionAttachmentURL(attachment)
		if !ok {
			continue
		}
		attachmentType := strings.TrimSpace(attachment.Type)
		if attachmentType == "" {
			attachmentType = sessionAttachmentType(attachment)
		}
		attachments = append(attachments, Attachment{
			Type:        attachmentType,
			URL:         urlValue,
			Filename:    strings.TrimSpace(attachment.Filename),
			ContentType: strings.TrimSpace(attachment.ContentType),
		})
	}

	if len(attachments) == 0 {
		return nil
	}
	return attachments
}

func sessionAttachmentURL(attachment providers.Attachment) (string, bool) {
	if rawURL := strings.TrimSpace(attachment.URL); rawURL != "" {
		return rawURL, true
	}

	ref := strings.TrimSpace(attachment.Ref)
	if ref == "" {
		return "", false
	}
	if strings.HasPrefix(ref, "media://") {
		// Persisted session history must only expose durable attachment locations.
		// media:// refs depend on the live in-memory MediaStore and may stop
		// resolving after a restart or cleanup, so omit them from reopened history.
		return "", false
	}
	return ref, true
}

func sessionAttachmentType(attachment providers.Attachment) string {
	contentType := strings.ToLower(strings.TrimSpace(attachment.ContentType))
	filename := strings.ToLower(strings.TrimSpace(attachment.Filename))
	rawRef := strings.ToLower(strings.TrimSpace(attachment.Ref))
	rawURL := strings.ToLower(strings.TrimSpace(attachment.URL))

	switch {
	case strings.HasPrefix(contentType, "image/"),
		strings.HasPrefix(rawRef, "data:image/"),
		strings.HasPrefix(rawURL, "data:image/"):
		return "image"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	}

	switch ext := filepath.Ext(filename); ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".svg":
		return "image"
	case ".mp3", ".wav", ".ogg", ".m4a", ".flac", ".aac", ".wma", ".opus":
		return "audio"
	case ".mp4", ".avi", ".mov", ".webm", ".mkv":
		return "video"
	default:
		return "file"
	}
}

func assistantMessageInternalOnly(msg providers.Message) bool {
	return strings.TrimSpace(msg.Content) == handledToolResponseSummaryText
}

func assistantThoughtMessage(msg providers.Message) (Message, bool) {
	reasoning := strings.TrimSpace(msg.ReasoningContent)
	if reasoning == "" {
		return Message{}, false
	}
	if reasoning == strings.TrimSpace(msg.Content) {
		return Message{}, false
	}
	return Message{
		Role:      "assistant",
		Content:   reasoning,
		Kind:      "thought",
		ModelName: msg.ModelName,
		CreatedAt: msg.CreatedAt,
	}, true
}

func assistantToolCallsMessage(
	toolCalls []providers.ToolCall,
	modelName string,
	toolFeedbackMaxArgsLength int,
	createdAt *time.Time,
) (Message, bool) {
	if len(toolCalls) == 0 {
		return Message{}, false
	}
	if toolFeedbackMaxArgsLength <= 0 {
		toolFeedbackMaxArgsLength = defaultToolFeedbackMaxArgsLength()
	}

	visibleToolCalls := utils.BuildVisibleToolCalls(toolCalls, toolFeedbackMaxArgsLength)
	if len(visibleToolCalls) == 0 {
		return Message{}, false
	}

	return Message{
		Role:      "assistant",
		Kind:      "tool_calls",
		ModelName: modelName,
		CreatedAt: createdAt,
		ToolCalls: visibleToolCalls,
	}, true
}

func visibleAssistantToolMessages(
	toolCalls []providers.ToolCall,
	modelName string,
	createdAt *time.Time,
) []Message {
	if len(toolCalls) == 0 {
		return nil
	}

	messages := make([]Message, 0, len(toolCalls))
	for _, tc := range toolCalls {
		name, argsJSON := utils.VisibleToolCallNameAndArguments(tc)
		if name != "message" {
			continue
		}
		content, ok := parseMessageToolContent(argsJSON)
		if !ok {
			continue
		}
		messages = append(messages, Message{
			Role:      "assistant",
			Content:   content,
			ModelName: modelName,
			CreatedAt: createdAt,
		})
	}

	return messages
}

func parseMessageToolContent(argsJSON string) (string, bool) {
	var args struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", false
	}
	if strings.TrimSpace(args.Content) == "" {
		return "", false
	}
	return args.Content, true
}

// sessionListCandidate is a web chat session that may be listed, known from
// its metadata and the size of its history file alone.
type sessionListCandidate struct {
	ref     webChatJSONLSessionRef
	updated time.Time
}

// listWebChatSessions returns one page of web chat sessions, most recently
// updated first. Sessions are ordered and paged by their metadata, so only the
// histories of the sessions on the page are read.
func listWebChatSessions(dir string, offset, limit, toolFeedbackMaxArgsLength int) []Summary {
	items := []Summary{}
	refs, err := findWebChatJSONLSessions(dir)
	if err != nil {
		// The directory doesn't exist yet: no sessions.
		return items
	}

	candidates := make([]sessionListCandidate, 0, len(refs))
	for _, ref := range refs {
		info, statErr := os.Stat(filepath.Join(dir, sanitizeSessionKey(ref.Key)) + ".jsonl")
		if statErr != nil || !sessionMayHaveContent(ref.meta, info.Size()) {
			continue
		}
		_, updated := sessionTimes(ref.meta, info.ModTime())
		candidates = append(candidates, sessionListCandidate{ref: ref, updated: updated})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if !candidates[i].updated.Equal(candidates[j].updated) {
			return candidates[i].updated.After(candidates[j].updated)
		}
		return candidates[i].ref.ID < candidates[j].ref.ID
	})

	if offset >= len(candidates) {
		return items
	}
	for _, candidate := range candidates[offset:] {
		if len(items) >= limit {
			break
		}
		sess, loadErr := readJSONLSessionWithMeta(dir, candidate.ref.Key, candidate.ref.meta)
		if loadErr != nil || isEmptySession(sess) {
			// Metadata can't tell a history of only unreadable lines from a
			// real one; the next session takes this one's place.
			continue
		}
		items = append(items, buildSessionListItem(candidate.ref.ID, sess, toolFeedbackMaxArgsLength))
	}
	return items
}

// sessionMayHaveContent reports, from a session's metadata and the size of
// its history file, whether the session can have anything to show.
func sessionMayHaveContent(meta memory.SessionMeta, historySize int64) bool {
	if strings.TrimSpace(meta.Summary) != "" {
		return true
	}
	if historySize == 0 {
		return false
	}
	// The store moves Skip past every line it truncates and counts every line
	// it appends. A zero count is metadata from before the history was
	// written, so it proves nothing.
	return meta.Count == 0 || meta.Skip < meta.Count
}
