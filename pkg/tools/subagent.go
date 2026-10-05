package tools

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/xibodev/compa/pkg/providers"
)

// SubTurnSpawner is an interface for spawning sub-turns.
// This avoids circular dependency between tools and agent packages.
type SubTurnSpawner interface {
	SpawnSubTurn(ctx context.Context, cfg SubTurnConfig) (*ToolResult, error)
}

// SubTurnConfig holds configuration for spawning a sub-turn. A sub-turn runs
// on its agent's model: the spawning agent's — or, with TargetAgentID, the
// target agent's — with that agent's tools and model settings.
type SubTurnConfig struct {
	SystemPrompt       string
	Async              bool          // true for async (spawn), false for sync (subagent)
	Critical           bool          // continue running after parent finishes gracefully
	Timeout            time.Duration // 0 = use default (5 minutes)
	ActualSystemPrompt string
	InitialMessages    []providers.Message
	TargetAgentID      string // If set, run as this agent (its workspace, model, tools)
}

type SubagentTask struct {
	ID            string
	Task          string
	Label         string
	AgentID       string
	OriginChannel string
	OriginChatID  string
	Status        string
	Result        string
	Created       int64

	// seq orders tasks started within the same millisecond.
	seq int
}

// SubagentManager holds the task registry reported by spawn_status, shared by
// the spawn and subagent tools.
type SubagentManager struct {
	tasks  map[string]*SubagentTask
	mu     sync.RWMutex
	nextID int
}

// maxFinishedSubagentTasks bounds how many finished tasks spawn_status keeps;
// running tasks are always kept.
const maxFinishedSubagentTasks = 100

// maxSubagentTaskResultRunes bounds the result kept for spawn_status, which
// shows only the start of it.
const maxSubagentTaskResultRunes = 2000

func NewSubagentManager() *SubagentManager {
	return &SubagentManager{
		tasks: make(map[string]*SubagentTask),
	}
}

// startTask records a task that is starting to run and returns its ID.
func (sm *SubagentManager) startTask(task, label, agentID, originChannel, originChatID string) string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.nextID++
	id := fmt.Sprintf("subagent-%d", sm.nextID)
	sm.tasks[id] = &SubagentTask{
		ID:            id,
		Task:          task,
		Label:         label,
		AgentID:       agentID,
		OriginChannel: originChannel,
		OriginChatID:  originChatID,
		Status:        "running",
		Created:       time.Now().UnixMilli(),
		seq:           sm.nextID,
	}
	sm.pruneLocked()
	return id
}

// finishTask records how a task ended: completed, failed or canceled.
func (sm *SubagentManager) finishTask(id, status, result string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	task, ok := sm.tasks[id]
	if !ok {
		return
	}
	task.Status = status
	if cut, rest := runePrefix(result, maxSubagentTaskResultRunes); rest > 0 {
		result = cut + "…"
	}
	task.Result = result
	sm.pruneLocked()
}

// pruneLocked drops the oldest finished tasks beyond the limit.
func (sm *SubagentManager) pruneLocked() {
	finished := make([]*SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		if task.Status != "running" {
			finished = append(finished, task)
		}
	}
	if len(finished) <= maxFinishedSubagentTasks {
		return
	}
	sort.Slice(finished, func(i, j int) bool {
		if finished[i].Created != finished[j].Created {
			return finished[i].Created < finished[j].Created
		}
		return finished[i].seq < finished[j].seq
	})
	for _, task := range finished[:len(finished)-maxFinishedSubagentTasks] {
		delete(sm.tasks, task.ID)
	}
}

// GetTaskCopy returns a copy of the task with the given ID, taken under the
// read lock, so the caller receives a consistent snapshot with no data race.
func (sm *SubagentManager) GetTaskCopy(taskID string) (SubagentTask, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	task, ok := sm.tasks[taskID]
	if !ok {
		return SubagentTask{}, false
	}
	return *task, true
}

// ListTaskCopies returns value copies of all tasks, taken under the read lock,
// so callers receive consistent snapshots with no data race.
func (sm *SubagentManager) ListTaskCopies() []SubagentTask {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	copies := make([]SubagentTask, 0, len(sm.tasks))
	for _, task := range sm.tasks {
		copies = append(copies, *task)
	}
	return copies
}

// SubagentTool executes a subagent task synchronously and returns the result.
// It directly calls SubTurnSpawner with Async=false for synchronous execution.
// The child runs as an agent, with that agent's own model settings.
type SubagentTool struct {
	spawner SubTurnSpawner
}

func NewSubagentTool(manager *SubagentManager) *SubagentTool {
	return &SubagentTool{}
}

// SetSpawner sets the SubTurnSpawner for direct sub-turn execution.
func (t *SubagentTool) SetSpawner(spawner SubTurnSpawner) {
	t.spawner = spawner
}

func (t *SubagentTool) Name() string {
	return "subagent"
}

func (t *SubagentTool) Description() string {
	return "Execute a subagent task synchronously and return the result. Use this for delegating specific tasks to an independent agent instance. Returns execution summary to user and full details to LLM."
}

func (t *SubagentTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"task": map[string]any{
				"type":        "string",
				"description": "The task for subagent to complete",
			},
			"label": map[string]any{
				"type":        "string",
				"description": "Optional short label for the task (for display)",
			},
		},
		"required": []string{"task"},
	}
}

func (t *SubagentTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	task, ok := args["task"].(string)
	if !ok {
		return ErrorResult("task is required").WithError(fmt.Errorf("task parameter is required"))
	}

	label, ok := args["label"].(string)
	if !ok {
		label = ""
	}

	// Build system prompt for subagent
	systemPrompt := fmt.Sprintf(
		`You are a subagent. Complete the given task independently and provide a clear, concise result.

Task: %s`,
		task,
	)

	if label != "" {
		systemPrompt = fmt.Sprintf(
			`You are a subagent labeled "%s". Complete the given task independently and provide a clear, concise result.

Task: %s`,
			label,
			task,
		)
	}

	// Use spawner if available (direct SpawnSubTurn call)
	if t.spawner != nil {
		result, err := t.spawner.SpawnSubTurn(ctx, SubTurnConfig{
			SystemPrompt: systemPrompt,
			Async:        false, // Synchronous execution
		})
		if err != nil {
			return ErrorResult(fmt.Sprintf("Subagent execution failed: %v", err)).WithError(err)
		}

		// Format result for display
		userContent := result.ForLLM
		if result.ForUser != "" {
			userContent = result.ForUser
		}
		maxUserLen := 500
		if len(userContent) > maxUserLen {
			userContent = userContent[:maxUserLen] + "..."
		}

		labelStr := label
		if labelStr == "" {
			labelStr = "(unnamed)"
		}
		llmContent := fmt.Sprintf("Subagent task completed:\nLabel: %s\nResult: %s",
			labelStr, result.ForLLM)

		return &ToolResult{
			ForLLM:  llmContent,
			ForUser: userContent,
			Silent:  false,
			IsError: result.IsError,
			Async:   false,
		}
	}

	// Fallback: spawner not configured
	return ErrorResult("Subagent manager not configured").WithError(fmt.Errorf("spawner not set"))
}
