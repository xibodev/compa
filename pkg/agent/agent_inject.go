// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"strings"

	"github.com/xibodev/compa/v3/pkg/agent/interfaces"
	"github.com/xibodev/compa/v3/pkg/audio/asr"
	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/channels"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/constants"
	"github.com/xibodev/compa/v3/pkg/logger"
	"github.com/xibodev/compa/v3/pkg/media"
	"github.com/xibodev/compa/v3/pkg/providers"
	"github.com/xibodev/compa/v3/pkg/state"
	"github.com/xibodev/compa/v3/pkg/tools"
)

func (al *AgentLoop) RegisterTool(tool tools.Tool) {
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			agent.Tools.Register(tool)
		}
	}
}

// RegisterExtensionTool registers a tool contributed from outside Compa, such
// as a module's, on every agent. It never takes the place of a registered
// tool, and a built-in tool registered later under its name replaces it.
func (al *AgentLoop) RegisterExtensionTool(tool tools.Tool) {
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			agent.Tools.RegisterExtension(tool)
		}
	}
}

// SetChannelManager sets the channel manager turns send through. A reload
// may call it while turns run: they read it through currentChannelManager.
func (al *AgentLoop) SetChannelManager(cm *channels.Manager) {
	al.mu.Lock()
	defer al.mu.Unlock()
	if cm == nil {
		al.channelManager = nil
		return
	}
	al.channelManager = cm
}

// currentChannelManager returns the channel manager, or nil.
func (al *AgentLoop) currentChannelManager() interfaces.ChannelManager {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.channelManager
}

// currentMediaStore returns the media store, or nil.
func (al *AgentLoop) currentMediaStore() media.MediaStore {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.mediaStore
}

// currentContextManager returns the context manager a reload may replace.
func (al *AgentLoop) currentContextManager() ContextManager {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.contextManager
}

// currentFailover returns the failover a reload may replace.
func (al *AgentLoop) currentFailover() *providers.Failover {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.failover
}

func (al *AgentLoop) GetRegistry() *AgentRegistry {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.registry
}

func (al *AgentLoop) GetConfig() *config.Config {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return al.cfg
}

// SetMediaStore sets the media store of the loop and its tools. A reload may
// call it while turns run: they read it through currentMediaStore.
func (al *AgentLoop) SetMediaStore(s media.MediaStore) {
	al.mu.Lock()
	al.mediaStore = s
	al.mu.Unlock()

	// Propagate store to all registered tools that can emit media.
	registry := al.GetRegistry()
	for _, agentID := range registry.ListAgentIDs() {
		if agent, ok := registry.GetAgent(agentID); ok {
			agent.Tools.SetMediaStore(s)
		}
	}
	registry.ForEachTool("send_tts", func(t tools.Tool) {
		if st, ok := t.(*tools.SendTTSTool); ok {
			st.SetMediaStore(s)
		}
	})
}

func (al *AgentLoop) SetTranscriber(t asr.Transcriber) {
	al.transcriber = t
}

func (al *AgentLoop) SetReloadFunc(fn func() error) {
	al.reloadFunc = fn
}

func (al *AgentLoop) RecordLastChannel(channel string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChannel(channel)
}

func (al *AgentLoop) RecordLastChatID(chatID string) error {
	if al.state == nil {
		return nil
	}
	return al.state.SetLastChatID(chatID)
}

// StateManager returns the manager the loop records the last chat and the
// owner's chat in, or nil when it has no default agent. Services that report
// to those chats share it.
func (al *AgentLoop) StateManager() *state.Manager {
	return al.state
}

// recordOwnerChat remembers the chat of a turn the owner started, as the
// chat notices and approval requests go to. A turn without a sender, such as
// a scheduled one, or on a channel that can't receive messages, such as the
// terminal, leaves it unchanged.
func (al *AgentLoop) recordOwnerChat(ctx context.Context, inbound *bus.InboundContext) {
	if al.state == nil || inbound == nil || isScheduledTurn(ctx) || !inboundFromOwner(inbound) {
		return
	}
	channel := strings.TrimSpace(inbound.Channel)
	chatID := strings.TrimSpace(inbound.ChatID)
	if channel == "" || chatID == "" || constants.IsInternalChannel(channel) {
		return
	}
	if err := al.state.SetOwnerChat(channel, chatID); err != nil {
		logger.WarnCF("agent", "Failed to record the owner's chat", map[string]any{"error": err.Error()})
	}
}

func (al *AgentLoop) GetStartupInfo() map[string]any {
	info := make(map[string]any)

	registry := al.GetRegistry()
	agent := registry.GetDefaultAgent()
	if agent == nil {
		return info
	}

	// Tools info
	toolsList := agent.Tools.List()
	info["tools"] = map[string]any{
		"count": len(toolsList),
		"names": toolsList,
	}

	// Skills info
	info["skills"] = agent.ContextBuilder.GetSkillsInfo()

	// Agents info
	info["agents"] = map[string]any{
		"count": len(registry.ListAgentIDs()),
		"ids":   registry.ListAgentIDs(),
	}

	return info
}
