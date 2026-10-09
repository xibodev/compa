package heartbeat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/pkg/bus"
	"github.com/xibodev/compa/v4/pkg/state"
	"github.com/xibodev/compa/v4/pkg/tools"
)

// newTestService returns an enabled service whose owner last wrote from a
// telegram chat, with HEARTBEAT.md holding one task.
func newTestService(t *testing.T) (*HeartbeatService, string) {
	t.Helper()
	tmpDir := t.TempDir()
	hs := NewHeartbeatService(tmpDir, 30, true)
	hs.stopChan = make(chan struct{}) // Enable for testing
	if err := state.NewManager(tmpDir).SetOwnerChat("telegram", "123"); err != nil {
		t.Fatalf("SetOwnerChat: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "HEARTBEAT.md"), []byte("Test task"), 0o644); err != nil {
		t.Fatalf("write HEARTBEAT.md: %v", err)
	}
	return hs, tmpDir
}

func TestExecuteHeartbeat_Async(t *testing.T) {
	hs, _ := newTestService(t)

	asyncCalled := false
	asyncResult := &tools.ToolResult{
		ForLLM:  "Background task started",
		ForUser: "Task started in background",
		Silent:  false,
		IsError: false,
		Async:   true,
	}

	hs.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
		asyncCalled = true
		if prompt == "" {
			t.Error("Expected non-empty prompt")
		}
		return asyncResult
	})

	// Execute heartbeat directly (internal method for testing)
	hs.executeHeartbeat()

	if !asyncCalled {
		t.Error("Expected handler to be called")
	}
}

func TestExecuteHeartbeat_ResultLogging(t *testing.T) {
	tests := []struct {
		name    string
		result  *tools.ToolResult
		wantLog string
	}{
		{
			name: "error result",
			result: &tools.ToolResult{
				ForLLM:  "Heartbeat failed: connection error",
				ForUser: "",
				Silent:  false,
				IsError: true,
				Async:   false,
			},
			wantLog: "Heartbeat error",
		},
		{
			name: "silent result",
			result: &tools.ToolResult{
				ForLLM:  "Heartbeat completed successfully",
				ForUser: "",
				Silent:  true,
				IsError: false,
				Async:   false,
			},
			wantLog: "silent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hs, tmpDir := newTestService(t)

			hs.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
				return tt.result
			})

			hs.executeHeartbeat()

			logFile := filepath.Join(tmpDir, "heartbeat.log")
			data, err := os.ReadFile(logFile)
			if err != nil {
				t.Fatalf("Failed to read log file: %v", err)
			}
			if !strings.Contains(string(data), tt.wantLog) {
				t.Errorf("log = %q, want it to contain %q", data, tt.wantLog)
			}
		})
	}
}

func TestHeartbeatService_StartStop(t *testing.T) {
	tmpDir := t.TempDir()

	hs := NewHeartbeatService(tmpDir, 1, true)

	err := hs.Start()
	if err != nil {
		t.Fatalf("Failed to start heartbeat service: %v", err)
	}

	hs.Stop()

	time.Sleep(100 * time.Millisecond)
}

func TestHeartbeatService_Disabled(t *testing.T) {
	tmpDir := t.TempDir()

	hs := NewHeartbeatService(tmpDir, 1, false)

	if hs.enabled != false {
		t.Error("Expected service to be disabled")
	}

	err := hs.Start()
	_ = err // Disabled service returns nil
}

func TestExecuteHeartbeat_NilResult(t *testing.T) {
	hs, _ := newTestService(t)

	hs.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
		return nil
	})

	// Should not panic with nil result
	hs.executeHeartbeat()
}

// TestLogPath verifies heartbeat log is written to workspace directory
func TestLogPath(t *testing.T) {
	tmpDir := t.TempDir()

	hs := NewHeartbeatService(tmpDir, 30, true)

	// Write a log entry
	hs.logf("INFO", "Test log entry")

	// Verify log file exists at workspace root
	expectedLogPath := filepath.Join(tmpDir, "heartbeat.log")
	if _, err := os.Stat(expectedLogPath); os.IsNotExist(err) {
		t.Errorf("Expected log file at %s, but it doesn't exist", expectedLogPath)
	}
}

func TestLogRotatesAtCap(t *testing.T) {
	tmpDir := t.TempDir()
	hs := NewHeartbeatService(tmpDir, 30, true)
	logFile := filepath.Join(tmpDir, "heartbeat.log")
	if err := os.WriteFile(logFile, make([]byte, maxLogBytes), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	hs.logf("INFO", "%s", strings.Repeat("x", 3*maxLogEntryRunes))

	info, err := os.Stat(logFile)
	if err != nil {
		t.Fatalf("stat log: %v", err)
	}
	if info.Size() > 3*maxLogEntryRunes {
		t.Fatalf("log size after rotation = %d", info.Size())
	}
	if rotated, err := os.Stat(logFile + ".1"); err != nil || rotated.Size() != maxLogBytes {
		t.Fatalf("rotated log = %v, %v", rotated, err)
	}
}

// TestHeartbeatFilePath verifies HEARTBEAT.md is at workspace root
func TestHeartbeatFilePath(t *testing.T) {
	tmpDir := t.TempDir()

	hs := NewHeartbeatService(tmpDir, 30, true)

	// Trigger default template creation
	hs.buildPrompt()

	// Verify HEARTBEAT.md exists at workspace root
	expectedPath := filepath.Join(tmpDir, "HEARTBEAT.md")
	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Expected HEARTBEAT.md at %s: %v", expectedPath, err)
	}
	if strings.Contains(string(data), "MaixCam") {
		t.Error("default template still mentions MaixCam")
	}
}

func TestBuildPrompt_DefaultTemplateStaysIdle(t *testing.T) {
	tmpDir := t.TempDir()

	hs := NewHeartbeatService(tmpDir, 30, true)
	hs.createDefaultHeartbeatTemplate()

	if prompt := hs.buildPrompt(); prompt != "" {
		t.Fatalf("buildPrompt() = %q, want empty prompt for untouched default template", prompt)
	}
}

func TestBuildPrompt_UserTasksAfterMarkerProducePrompt(t *testing.T) {
	tmpDir := t.TempDir()

	hs := NewHeartbeatService(tmpDir, 30, true)
	hs.createDefaultHeartbeatTemplate()

	path := filepath.Join(tmpDir, "HEARTBEAT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Failed to read HEARTBEAT.md: %v", err)
	}
	updated := string(data) + "\n- Check unread Feishu messages\n"
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatalf("Failed to update HEARTBEAT.md: %v", err)
	}

	prompt := hs.buildPrompt()
	if prompt == "" {
		t.Fatal("buildPrompt() = empty, want non-empty prompt when user tasks are present")
	}
	if !strings.Contains(prompt, "Check unread Feishu messages") {
		t.Fatalf("prompt = %q, want user task content", prompt)
	}
}

func receiveOutbound(t *testing.T, msgBus *bus.MessageBus) (bus.OutboundMessage, bool) {
	t.Helper()
	select {
	case msg := <-msgBus.OutboundChan():
		return msg, true
	case <-time.After(200 * time.Millisecond):
		return bus.OutboundMessage{}, false
	}
}

func TestExecuteHeartbeat_DeliversNonOKReply(t *testing.T) {
	hs, tmpDir := newTestService(t)
	// Someone else wrote last; the reply still goes to the owner's chat.
	if err := state.NewManager(tmpDir).SetLastChannel("discord:999"); err != nil {
		t.Fatalf("SetLastChannel: %v", err)
	}
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	hs.SetBus(msgBus)

	var gotChannel, gotChat string
	hs.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
		gotChannel, gotChat = channel, chatID
		return tools.NewToolResult("Disk is 95% full")
	})
	hs.executeHeartbeat()

	if gotChannel != "telegram" || gotChat != "123" {
		t.Fatalf("handler ran for %s:%s, want telegram:123", gotChannel, gotChat)
	}
	msg, ok := receiveOutbound(t, msgBus)
	if !ok {
		t.Fatal("non-OK heartbeat reply was not delivered")
	}
	if msg.Context.Channel != "telegram" || msg.Context.ChatID != "123" || msg.Content != "Disk is 95% full" {
		t.Fatalf("delivered %+v", msg)
	}
}

func TestExecuteHeartbeat_OKReplyIsNotDelivered(t *testing.T) {
	hs, _ := newTestService(t)
	msgBus := bus.NewMessageBus()
	defer msgBus.Close()
	hs.SetBus(msgBus)

	hs.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
		return tools.NewToolResult(" HEARTBEAT_OK\n")
	})
	hs.executeHeartbeat()

	if msg, ok := receiveOutbound(t, msgBus); ok {
		t.Fatalf("HEARTBEAT_OK was delivered: %+v", msg)
	}
}

func TestExecuteHeartbeat_SkipsWithoutKnownChat(t *testing.T) {
	for _, owner := range [][2]string{{"", ""}, {"cli", "direct"}} {
		hs, tmpDir := newTestService(t)
		sm := state.NewManager(tmpDir)
		if err := sm.SetOwnerChat(owner[0], owner[1]); err != nil {
			t.Fatalf("SetOwnerChat: %v", err)
		}
		// The last chat isn't the owner's: someone else's chat gets nothing.
		if err := sm.SetLastChannel("telegram:999"); err != nil {
			t.Fatalf("SetLastChannel: %v", err)
		}
		msgBus := bus.NewMessageBus()
		hs.SetBus(msgBus)
		called := false
		hs.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
			called = true
			return tools.NewToolResult("ran")
		})
		hs.executeHeartbeat()
		if called {
			t.Fatalf("heartbeat ran with owner chat %q; it must not fall back to another identity", owner)
		}
		if msg, ok := receiveOutbound(t, msgBus); ok {
			t.Fatalf("heartbeat delivered %+v without a known owner chat", msg)
		}
		msgBus.Close()
	}
}

func TestExecuteHeartbeat_UsesCurrentState(t *testing.T) {
	hs, tmpDir := newTestService(t)

	// The owner writes from a newer chat after the service was created.
	if err := state.NewManager(tmpDir).SetOwnerChat("discord", "456"); err != nil {
		t.Fatalf("SetOwnerChat: %v", err)
	}
	var got string
	hs.SetHandler(func(prompt, channel, chatID string) *tools.ToolResult {
		got = channel + ":" + chatID
		return tools.SilentResult("ok")
	})
	hs.executeHeartbeat()
	if got != "discord:456" {
		t.Fatalf("heartbeat ran for %q, want the owner's current chat discord:456", got)
	}

	// A shared manager is used as is.
	shared := state.NewManager(t.TempDir())
	if err := shared.SetOwnerChat("slack", "789"); err != nil {
		t.Fatalf("SetOwnerChat: %v", err)
	}
	hs.SetStateManager(shared)
	hs.executeHeartbeat()
	if got != "slack:789" {
		t.Fatalf("heartbeat ran for %q, want the shared manager's owner chat slack:789", got)
	}
}

func TestFirstDelay_NoImmediateHeartbeatOnReload(t *testing.T) {
	tmpDir := t.TempDir()
	first := NewHeartbeatService(tmpDir, 30, true)
	if d := first.firstDelay(time.Now()); d != firstRunDelay {
		t.Fatalf("first start delay = %v, want %v", d, firstRunDelay)
	}

	// A reload builds a new service for the same workspace.
	reloaded := NewHeartbeatService(tmpDir, 30, true)
	if d := reloaded.firstDelay(time.Now()); d != 30*time.Minute {
		t.Fatalf("reload delay before any heartbeat = %v, want the interval", d)
	}

	reloaded.recordRun(time.Now().Add(-10 * time.Minute))
	again := NewHeartbeatService(tmpDir, 30, true)
	if d := again.firstDelay(time.Now()); d < 19*time.Minute || d > 20*time.Minute {
		t.Fatalf("reload delay after a heartbeat 10m ago = %v, want about 20m", d)
	}
}
