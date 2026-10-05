// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"github.com/xibodev/compa/pkg/agent/interfaces"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/media"
	"github.com/xibodev/compa/pkg/providers"
)

// Pipeline holds the runtime dependencies used by Pipeline methods.
// It is constructed by runTurn via NewPipeline and passed to sub-methods
// so that the coordinator can delegate phase execution.
type Pipeline struct {
	Bus            interfaces.MessageBus
	Cfg            *config.Config
	ContextManager ContextManager
	Hooks          *HookManager
	Failover       *providers.Failover
	ChannelManager interfaces.ChannelManager
	MediaStore     media.MediaStore
	al             *AgentLoop
}

// NewPipeline creates a Pipeline from an AgentLoop instance: a snapshot of
// the loop's config, failover, context manager, channel manager and media
// store, read together under the loop's lock. A turn runs on its snapshot
// even when a reload replaces them meanwhile.
func NewPipeline(al *AgentLoop) *Pipeline {
	al.mu.RLock()
	defer al.mu.RUnlock()
	return &Pipeline{
		Bus:            al.bus,
		Cfg:            al.cfg,
		ContextManager: al.contextManager,
		Hooks:          al.hooks,
		Failover:       al.failover,
		ChannelManager: al.channelManager,
		MediaStore:     al.mediaStore,
		al:             al,
	}
}
