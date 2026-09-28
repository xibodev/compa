package commands

import (
	"context"

	"github.com/xibodev/compa/pkg/config"
)

type MCPServerInfo struct {
	Name      string
	Enabled   bool
	Deferred  bool
	Connected bool
	ToolCount int
}

type MCPToolParameterInfo struct {
	Name        string
	Type        string
	Description string
	Required    bool
}

type MCPToolInfo struct {
	Name        string
	Description string
	Parameters  []MCPToolParameterInfo
}

// ContextStats describes current session context window usage.
type ContextStats struct {
	UsedTokens        int
	TotalTokens       int // model context window
	HistoryTokens     int // history-only tokens (what maybeSummarize checks)
	CompressAtTokens  int // hard budget compression threshold
	SummarizeAtTokens int // soft summarization trigger
	UsedPercent       int // 0-100
	MessageCount      int
}

// StopResult describes the outcome of a stop request for the current session.
type StopResult struct {
	Stopped  bool
	TaskName string
}

// ModelInfo describes the model the current agent runs on.
type ModelInfo struct {
	// Selection is the agent's model selection: an exact target
	// "instance-id/model-id" or a model route name. Empty when none is set.
	Selection string
	// ServedTarget is the exact target that last answered the agent, when
	// known.
	ServedTarget string
	// Unavailable is why Selection does not resolve; empty when it does.
	Unavailable string
}

// Runtime provides runtime dependencies to command handlers. It is constructed
// per-request by the agent loop so that per-request state (like session scope)
// can coexist with long-lived callbacks (like GetModelInfo).
type Runtime struct {
	Config             *config.Config
	GetModelInfo       func() ModelInfo
	AskSideQuestion    func(ctx context.Context, question string) (string, error)
	ListAgentIDs       func() []string
	ListDefinitions    func() []Definition
	ListSkillNames     func() []string
	ListMCPServers     func(ctx context.Context) []MCPServerInfo
	ListMCPTools       func(ctx context.Context, serverName string) ([]MCPToolInfo, error)
	GetEnabledChannels func() []string
	GetActiveTurn      func() any // Returning any to avoid circular dependency with agent package
	GetContextStats    func() *ContextStats
	// SwitchModel switches the current agent to a model selection (an exact
	// target or a route name) and returns the previous selection.
	SwitchModel    func(selection string) (previous string, err error)
	SwitchChannel  func(value string) error
	ClearHistory   func() error
	ReloadConfig   func() error
	StopActiveTurn func() (StopResult, error)
}
