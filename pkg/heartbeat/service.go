// Compa - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package heartbeat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/constants"
	"github.com/xibodev/compa/v4/pkg/fileutil"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/state"
	"github.com/xibodev/compa/v4/pkg/tools"
)

const (
	minIntervalMinutes     = 5
	defaultIntervalMinutes = 30
	userTasksMarker        = "Add your heartbeat tasks below this line:"
	// heartbeatOK is the reply that means "nothing needs attention".
	heartbeatOK = "HEARTBEAT_OK"
	// firstRunDelay is how soon the first heartbeat of a process runs.
	firstRunDelay = time.Second
	// maxLogBytes caps heartbeat.log; a full log moves to heartbeat.log.1.
	maxLogBytes = 1 << 20
	// maxLogEntryRunes keeps one reply from filling the log.
	maxLogEntryRunes = 2000
)

// HeartbeatHandler is the function type for handling heartbeat.
// It returns a ToolResult that can indicate async operations.
// channel and chatID are derived from the last active user channel.
type HeartbeatHandler func(prompt, channel, chatID string) *tools.ToolResult

// HeartbeatService manages periodic heartbeat checks
type HeartbeatService struct {
	workspace string
	bus       *bus.MessageBus
	// state is the manager the agent loop records the owner's chat in. When
	// it isn't set, each heartbeat reads state.json afresh.
	state    *state.Manager
	handler  HeartbeatHandler
	interval time.Duration
	enabled  bool
	mu       sync.RWMutex
	stopChan chan struct{}
	// logMu serializes writes and rotation of heartbeat.log.
	logMu sync.Mutex
}

// runRecord remembers, per workspace and for this process, whether a
// heartbeat service already started and when a heartbeat last ran. A config
// reload replaces the service; the record keeps the reload from firing an
// immediate heartbeat.
type runRecord struct {
	lastRun time.Time
}

var (
	runsMu sync.Mutex
	runs   = map[string]*runRecord{}
)

// NewHeartbeatService creates a new heartbeat service
func NewHeartbeatService(workspace string, intervalMinutes int, enabled bool) *HeartbeatService {
	// Apply minimum interval
	if intervalMinutes < minIntervalMinutes && intervalMinutes != 0 {
		intervalMinutes = minIntervalMinutes
	}

	if intervalMinutes == 0 {
		intervalMinutes = defaultIntervalMinutes
	}

	return &HeartbeatService{
		workspace: workspace,
		interval:  time.Duration(intervalMinutes) * time.Minute,
		enabled:   enabled,
	}
}

// SetBus sets the message bus for delivering heartbeat results.
func (hs *HeartbeatService) SetBus(msgBus *bus.MessageBus) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.bus = msgBus
}

// SetHandler sets the heartbeat handler.
func (hs *HeartbeatService) SetHandler(handler HeartbeatHandler) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.handler = handler
}

// SetStateManager makes the heartbeat read the owner's chat, which it runs for
// and reports to, from the state manager the agent loop records it in.
func (hs *HeartbeatService) SetStateManager(sm *state.Manager) {
	hs.mu.Lock()
	defer hs.mu.Unlock()
	hs.state = sm
}

// Start begins the heartbeat service
func (hs *HeartbeatService) Start() error {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan != nil {
		logger.InfoC("heartbeat", "Heartbeat service already running")
		return nil
	}

	if !hs.enabled {
		logger.InfoC("heartbeat", "Heartbeat service disabled")
		return nil
	}

	hs.stopChan = make(chan struct{})
	go hs.runLoop(hs.stopChan, hs.firstDelay(time.Now()))

	logger.InfoCF("heartbeat", "Heartbeat service started", map[string]any{
		"interval_minutes": hs.interval.Minutes(),
	})

	return nil
}

// Stop gracefully stops the heartbeat service
func (hs *HeartbeatService) Stop() {
	hs.mu.Lock()
	defer hs.mu.Unlock()

	if hs.stopChan == nil {
		return
	}

	logger.InfoC("heartbeat", "Stopping heartbeat service")
	close(hs.stopChan)
	hs.stopChan = nil
}

// IsRunning returns whether the service is running
func (hs *HeartbeatService) IsRunning() bool {
	hs.mu.RLock()
	defer hs.mu.RUnlock()
	return hs.stopChan != nil
}

// firstDelay is the wait before the first heartbeat: short on the first start
// in this process, and the remainder of the interval after a reload, which
// replaces the service with a new one.
func (hs *HeartbeatService) firstDelay(now time.Time) time.Duration {
	runsMu.Lock()
	defer runsMu.Unlock()

	key := filepath.Clean(hs.workspace)
	rec, started := runs[key]
	if !started {
		runs[key] = &runRecord{}
		return firstRunDelay
	}
	if rec.lastRun.IsZero() {
		return hs.interval
	}
	return max(rec.lastRun.Add(hs.interval).Sub(now), firstRunDelay)
}

func (hs *HeartbeatService) recordRun(now time.Time) {
	runsMu.Lock()
	defer runsMu.Unlock()
	key := filepath.Clean(hs.workspace)
	if rec := runs[key]; rec != nil {
		rec.lastRun = now
	} else {
		runs[key] = &runRecord{lastRun: now}
	}
}

// runLoop runs the heartbeat timer
func (hs *HeartbeatService) runLoop(stopChan chan struct{}, firstDelay time.Duration) {
	timer := time.NewTimer(firstDelay)
	defer timer.Stop()

	for {
		select {
		case <-stopChan:
			return
		case <-timer.C:
			hs.executeHeartbeat()
			timer.Reset(hs.interval)
		}
	}
}

// executeHeartbeat performs a single heartbeat check
func (hs *HeartbeatService) executeHeartbeat() {
	hs.mu.RLock()
	enabled := hs.enabled
	handler := hs.handler
	if !hs.enabled || hs.stopChan == nil {
		hs.mu.RUnlock()
		return
	}
	hs.mu.RUnlock()

	if !enabled {
		return
	}

	hs.recordRun(time.Now())
	logger.DebugC("heartbeat", "Executing heartbeat")

	prompt := hs.buildPrompt()
	if prompt == "" {
		logger.InfoC("heartbeat", "No heartbeat prompt (HEARTBEAT.md empty or missing)")
		return
	}

	if handler == nil {
		hs.logErrorf("Heartbeat handler not configured")
		return
	}

	// The heartbeat runs as, and reports to, the chat the owner last wrote
	// from. Without one it doesn't run, so nothing is delivered: falling
	// back to the internal CLI identity would grant local privileges to
	// unattended work, and another chat's people aren't the owner.
	channel, chatID := hs.ownerChat()
	if channel == "" || chatID == "" {
		hs.logInfof("The owner's chat is not known yet; heartbeat skipped")
		logger.InfoC("heartbeat", "Heartbeat skipped: the owner's chat is not known yet")
		return
	}

	hs.logInfof("Reporting to the owner's chat: channel %s, chatID %s", channel, chatID)

	result := handler(prompt, channel, chatID)

	if result == nil {
		hs.logInfof("Heartbeat handler returned nil result")
		return
	}

	// Handle different result types
	if result.IsError {
		hs.logErrorf("Heartbeat error: %s", result.ForLLM)
		return
	}

	if result.Async {
		hs.logInfof("Async task started: %s", result.ForLLM)
		logger.InfoCF("heartbeat", "Async heartbeat task started",
			map[string]any{
				"message": result.ForLLM,
			})
		return
	}

	// Check if silent
	if result.Silent {
		hs.logInfof("Heartbeat OK - silent")
		return
	}

	reply := result.ForUser
	if reply == "" {
		reply = result.ForLLM
	}
	if isHeartbeatOK(reply) || strings.TrimSpace(reply) == "" {
		hs.logInfof("Heartbeat OK")
		return
	}

	hs.sendResponse(channel, chatID, reply)
	hs.logInfof("Heartbeat completed: %s", reply)
}

// ownerChat returns the chat the owner last wrote from, as the agent recorded
// it. Both are empty when none is known, or when it is an internal channel,
// which delivers nothing.
func (hs *HeartbeatService) ownerChat() (channel, chatID string) {
	hs.mu.RLock()
	sm := hs.state
	hs.mu.RUnlock()
	if sm == nil {
		// Without the agent's manager, the workspace's state file is read.
		sm = state.NewManager(hs.workspace)
	}
	channel, chatID = sm.GetOwnerChat()
	channel, chatID = strings.TrimSpace(channel), strings.TrimSpace(chatID)
	if channel == "" || chatID == "" || constants.IsInternalChannel(channel) {
		return "", ""
	}
	return channel, chatID
}

func isHeartbeatOK(reply string) bool {
	return strings.TrimSpace(reply) == heartbeatOK
}

// buildPrompt builds the heartbeat prompt from HEARTBEAT.md
func (hs *HeartbeatService) buildPrompt() string {
	heartbeatPath := filepath.Join(hs.workspace, "HEARTBEAT.md")

	data, err := os.ReadFile(heartbeatPath)
	if err != nil {
		if os.IsNotExist(err) {
			hs.createDefaultHeartbeatTemplate()
			return ""
		}
		hs.logErrorf("Error reading HEARTBEAT.md: %v", err)
		return ""
	}

	content := string(data)
	if !heartbeatHasUserTasks(content) {
		return ""
	}

	now := time.Now().Format("2006-01-02 15:04:05")
	return fmt.Sprintf(`# Heartbeat Check

Current time: %s

You are a proactive AI assistant. This is a scheduled heartbeat check.
Review the following tasks and execute any necessary actions using available skills.
If there is nothing that requires attention, respond ONLY with: HEARTBEAT_OK

%s
`, now, content)
}

// createDefaultHeartbeatTemplate creates the default HEARTBEAT.md file
func (hs *HeartbeatService) createDefaultHeartbeatTemplate() {
	heartbeatPath := filepath.Join(hs.workspace, "HEARTBEAT.md")

	defaultContent := `# Heartbeat Check List

This file contains tasks for the heartbeat service to check periodically.

## Examples

- Check for unread messages
- Review upcoming calendar events
- Check that the backup ran today

## Instructions

- Execute ALL tasks listed below. Do NOT skip any task.
- For simple tasks (e.g., report current time), respond directly.
- For complex tasks that may take time, use the spawn tool to create a subagent.
- The spawn tool is async - subagent results will be sent to the user automatically.
- After spawning a subagent, CONTINUE to process remaining tasks.
- Only respond with HEARTBEAT_OK when ALL tasks are done AND nothing needs attention.

---

Add your heartbeat tasks below this line:
`

	if err := fileutil.WriteFileAtomic(heartbeatPath, []byte(defaultContent), 0o644); err != nil {
		hs.logErrorf("Failed to create default HEARTBEAT.md: %v", err)
	} else {
		hs.logInfof("Created default HEARTBEAT.md template")
	}
}

func heartbeatHasUserTasks(content string) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}

	markerIdx := strings.Index(content, userTasksMarker)
	if markerIdx < 0 {
		return true
	}

	tasksSection := content[markerIdx+len(userTasksMarker):]
	for _, line := range strings.Split(tasksSection, "\n") {
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" {
			continue
		}
		if strings.HasPrefix(trimmedLine, "#") {
			continue
		}
		return true
	}

	return false
}

// sendResponse sends the heartbeat reply to the chat the heartbeat ran for.
func (hs *HeartbeatService) sendResponse(platform, chatID, response string) {
	hs.mu.RLock()
	msgBus := hs.bus
	hs.mu.RUnlock()

	if msgBus == nil {
		hs.logInfof("No message bus configured, heartbeat result not sent")
		return
	}

	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	if err := msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Context: bus.NewOutboundContext(platform, chatID, ""),
		Content: response,
		Notify:  true,
	}); err != nil {
		hs.logErrorf("Failed to send heartbeat result to %s: %v", platform, err)
		return
	}

	hs.logInfof("Heartbeat result sent to %s", platform)
}

// logInfof logs an informational message to the heartbeat log
func (hs *HeartbeatService) logInfof(format string, args ...any) {
	hs.logf("INFO", format, args...)
}

// logErrorf logs an error message to the heartbeat log
func (hs *HeartbeatService) logErrorf(format string, args ...any) {
	hs.logf("ERROR", format, args...)
}

// logf writes a message to the heartbeat log file. The log is capped at
// maxLogBytes: a full log moves to heartbeat.log.1, replacing the previous one.
func (hs *HeartbeatService) logf(level, format string, args ...any) {
	hs.logMu.Lock()
	defer hs.logMu.Unlock()

	logFile := filepath.Join(hs.workspace, "heartbeat.log")
	if info, err := os.Stat(logFile); err == nil && info.Size() >= maxLogBytes {
		_ = os.Rename(logFile, logFile+".1")
	}

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()

	message := fmt.Sprintf(format, args...)
	if utf8.RuneCountInString(message) > maxLogEntryRunes {
		message = string([]rune(message)[:maxLogEntryRunes]) + "…"
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(f, "[%s] [%s] %s\n", timestamp, level, message)
}
