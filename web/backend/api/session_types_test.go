package api

import "github.com/xibodev/compa/v3/pkg/session/history"

// The session tests decode responses into the shared history types.
type (
	sessionChatMessage = history.Message
	sessionListItem    = history.Summary
)

func sanitizeSessionKey(key string) string { return history.FileBase(key) }

// handledToolResponseSummaryText is the reply the agent records when a tool
// delivered the requested output (pkg/agent); transcripts leave it out.
const handledToolResponseSummaryText = "Requested output delivered via tool attachment."
