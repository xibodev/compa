// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// MemoryStore manages persistent memory for the agent.
// - Long-term memory: memory/MEMORY.md
// - Daily notes: memory/YYYYMM/YYYYMMDD.md
type MemoryStore struct {
	workspace  string
	memoryDir  string
	memoryFile string
}

// NewMemoryStore creates a new MemoryStore with the given workspace path.
// It ensures the memory directory exists.
func NewMemoryStore(workspace string) *MemoryStore {
	memoryDir := filepath.Join(workspace, "memory")
	memoryFile := filepath.Join(memoryDir, "MEMORY.md")

	// Ensure memory directory exists
	os.MkdirAll(memoryDir, 0o755)

	return &MemoryStore{
		workspace:  workspace,
		memoryDir:  memoryDir,
		memoryFile: memoryFile,
	}
}

// ReadLongTerm reads the long-term memory (MEMORY.md).
// Returns empty string if the file doesn't exist.
func (ms *MemoryStore) ReadLongTerm() string {
	if data, err := os.ReadFile(ms.memoryFile); err == nil {
		return string(data)
	}
	return ""
}

// recentDailyNoteDays is how many days of daily notes the prompt carries.
const recentDailyNoteDays = 3

// memoryContextLimit caps the memory injected into every prompt, in bytes:
// long-term memory first, then the most recent daily notes. Memory files grow
// without bound and every chat of the agent pays for them on every turn.
const memoryContextLimit = 24 << 10

// dailyNotePaths returns the daily note files of the days days up to now,
// newest first (memory/YYYYMM/YYYYMMDD.md).
func dailyNotePaths(memoryDir string, now time.Time, days int) []string {
	paths := make([]string, 0, days)
	for i := range days {
		dateStr := now.AddDate(0, 0, -i).Format("20060102") // YYYYMMDD
		paths = append(paths, filepath.Join(memoryDir, dateStr[:6], dateStr+".md"))
	}
	return paths
}

// GetRecentDailyNotes returns daily notes from the last N days.
// Contents are joined with "---" separator.
func (ms *MemoryStore) GetRecentDailyNotes(days int) string {
	var sb strings.Builder
	first := true

	for _, filePath := range dailyNotePaths(ms.memoryDir, time.Now(), days) {
		if data, err := os.ReadFile(filePath); err == nil {
			if !first {
				sb.WriteString("\n\n---\n\n")
			}
			sb.Write(data)
			first = false
		}
	}

	return sb.String()
}

// truncateForPrompt cuts text to at most limit bytes, on a rune boundary,
// and says how much of source was left out.
func truncateForPrompt(text string, limit int, source string) string {
	if len(text) <= limit {
		return text
	}
	cut := max(limit, 0)
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + fmt.Sprintf("\n\n[... %d more bytes of %s not shown]", len(text)-cut, source)
}

// GetMemoryContext returns formatted memory context for the agent prompt.
// Includes long-term memory and recent daily notes, together at most
// memoryContextLimit bytes.
func (ms *MemoryStore) GetMemoryContext() string {
	longTerm := truncateForPrompt(ms.ReadLongTerm(), memoryContextLimit, "memory/MEMORY.md")
	recentNotes := ms.GetRecentDailyNotes(recentDailyNoteDays)
	if recentNotes != "" {
		recentNotes = truncateForPrompt(recentNotes, memoryContextLimit-len(longTerm), "the daily notes")
	}

	if longTerm == "" && recentNotes == "" {
		return ""
	}

	var sb strings.Builder

	if longTerm != "" {
		sb.WriteString("## Long-term Memory\n\n")
		sb.WriteString(longTerm)
	}

	if recentNotes != "" {
		if longTerm != "" {
			sb.WriteString("\n\n---\n\n")
		}
		sb.WriteString("## Recent Daily Notes\n\n")
		sb.WriteString(recentNotes)
	}

	return sb.String()
}
