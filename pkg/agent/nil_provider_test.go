package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/xibodev/compa/v2/pkg/bus"
	"github.com/xibodev/compa/v2/pkg/config"
)

// A loop with no provider and no default model refuses the turn with the
// guidance to connect a provider and choose a model.
func TestProcessDirectWithoutConfiguredProviderFailsCleanly(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Agents.Defaults.Workspace = t.TempDir()
	loop := NewAgentLoop(cfg, bus.NewMessageBus(), nil)
	defer loop.Close()

	_, err := loop.ProcessDirect(context.Background(), "hello", "nil-provider")
	if err == nil || err.Error() != noModelSelectedMessage {
		t.Fatalf("ProcessDirect() error = %v, want %q", err, noModelSelectedMessage)
	}
	var noModel *noModelError
	if !errors.As(err, &noModel) {
		t.Fatalf("ProcessDirect() error = %T, want *noModelError", err)
	}
}
