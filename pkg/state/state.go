package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/fileutil"
	"github.com/xibodev/compa/v3/pkg/logger"
)

// State represents the persistent state for a workspace.
// It includes information about the last active channel/chat.
type State struct {
	// LastChannel is the last channel used for communication
	LastChannel string `json:"last_channel,omitempty"`

	// LastChatID is the last chat ID used for communication
	LastChatID string `json:"last_chat_id,omitempty"`

	// OwnerChannel and OwnerChatID name the chat the owner last wrote from:
	// where notices meant for the owner, and approval requests, go.
	OwnerChannel string `json:"owner_channel,omitempty"`
	OwnerChatID  string `json:"owner_chat_id,omitempty"`

	// Timestamp is the last time this state was updated
	Timestamp time.Time `json:"timestamp"`
}

// Manager manages persistent state with atomic saves.
//
// Several managers can share one state file — the agent's, the heartbeat's
// and the device service's — so a manager reads the file again on every get
// and before every set: each sees what the others wrote.
type Manager struct {
	workspace string
	state     *State
	mu        sync.Mutex
	stateFile string
}

// NewManager creates a new state manager for the given workspace.
func NewManager(workspace string) *Manager {
	stateDir := filepath.Join(workspace, "state")

	// Create state directory if it doesn't exist
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		logger.WarnCF("state", "failed to create state directory", map[string]any{
			"dir":   stateDir,
			"error": err.Error(),
		})
	}

	sm := &Manager{
		workspace: workspace,
		stateFile: filepath.Join(stateDir, "state.json"),
		state:     &State{},
	}
	if err := sm.load(); err != nil {
		logger.WarnCF("state", "failed to load state", map[string]any{
			"error": err.Error(),
		})
	}

	return sm
}

// SetLastChannel records the last channel and saves the state. The file is
// written only when the channel changed: every turn records its channel, and
// most turns come from the channel the previous one did.
func (sm *Manager) SetLastChannel(channel string) error {
	return sm.update(func(s *State) bool {
		if s.LastChannel == channel {
			return false
		}
		s.LastChannel = channel
		return true
	})
}

// SetLastChatID records the last chat ID and saves the state when it changed.
func (sm *Manager) SetLastChatID(chatID string) error {
	return sm.update(func(s *State) bool {
		if s.LastChatID == chatID {
			return false
		}
		s.LastChatID = chatID
		return true
	})
}

// SetOwnerChat records the chat the owner last wrote from, saving the state
// when it changed.
func (sm *Manager) SetOwnerChat(channel, chatID string) error {
	return sm.update(func(s *State) bool {
		if s.OwnerChannel == channel && s.OwnerChatID == chatID {
			return false
		}
		s.OwnerChannel = channel
		s.OwnerChatID = chatID
		return true
	})
}

// GetOwnerChat returns the chat the owner last wrote from; both are empty
// when none is known.
func (sm *Manager) GetOwnerChat() (channel, chatID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.refresh()
	return sm.state.OwnerChannel, sm.state.OwnerChatID
}

// GetLastChannel returns the last channel from the state.
func (sm *Manager) GetLastChannel() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.refresh()
	return sm.state.LastChannel
}

// GetLastChatID returns the last chat ID from the state.
func (sm *Manager) GetLastChatID() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.refresh()
	return sm.state.LastChatID
}

// GetTimestamp returns the timestamp of the last state update.
func (sm *Manager) GetTimestamp() time.Time {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.refresh()
	return sm.state.Timestamp
}

// update applies change to the current state and saves it when change
// reports that it changed something.
func (sm *Manager) update(change func(*State) bool) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.refresh()
	if !change(sm.state) {
		return nil
	}
	sm.state.Timestamp = time.Now()

	// Atomic save using temp file + rename
	if err := sm.saveAtomic(); err != nil {
		return fmt.Errorf("failed to save state atomically: %w", err)
	}
	return nil
}

// refresh reads the state file again, so a value another manager (or
// process) saved is seen. When the file cannot be read the state in memory
// stays as it is. Must be called with the lock held.
func (sm *Manager) refresh() {
	data, err := os.ReadFile(sm.stateFile)
	if err != nil {
		return
	}
	var current State
	if err := json.Unmarshal(data, &current); err != nil {
		return
	}
	*sm.state = current
}

// saveAtomic performs an atomic save using temp file + rename.
// This ensures that the state file is never corrupted:
// 1. Write to a temp file
// 2. Sync to disk (critical for SD cards/flash storage)
// 3. Rename temp file to target (atomic on POSIX systems)
// 4. If rename fails, cleanup the temp file
//
// Must be called with the lock held.
func (sm *Manager) saveAtomic() error {
	// Use unified atomic write utility with explicit sync for flash storage reliability.
	// Using 0o600 (owner read/write only) for secure default permissions.
	data, err := json.MarshalIndent(sm.state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal state: %w", err)
	}

	return fileutil.WriteFileAtomic(sm.stateFile, data, 0o600)
}

// load loads the state from disk.
func (sm *Manager) load() error {
	data, err := os.ReadFile(sm.stateFile)
	if err != nil {
		// File doesn't exist yet, that's OK
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read state file: %w", err)
	}

	if err := json.Unmarshal(data, sm.state); err != nil {
		return fmt.Errorf("failed to unmarshal state: %w", err)
	}

	return nil
}
