// Compa - Ultra-lightweight personal AI agent

package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/xibodev/compa/v2/pkg/tools"
)

// defaultToolTimeout bounds a tool call: a tool that hangs must not hold its
// turn, and with it its session, forever. A tool with a timeout setting of
// its own (tools.SelfTimed), such as exec or an MCP server's tools, is left to
// that timeout instead, which may be longer.
var defaultToolTimeout = 10 * time.Minute

// toolStopGrace is how long a tool whose deadline passed gets to return.
var toolStopGrace = 5 * time.Second

// executeToolWithTimeout runs a tool call under defaultToolTimeout. A tool
// that does not return soon after its deadline is left to finish on its own,
// and the call reports that it timed out. An async tool returns at once, and
// its background work keeps the context it was started with.
func executeToolWithTimeout(
	ctx context.Context,
	registry *tools.ToolRegistry,
	name string,
	args map[string]any,
	channel, chatID string,
	asyncCallback tools.AsyncCallback,
) *tools.ToolResult {
	tool, ok := registry.Get(name)
	_, isAsync := tool.(tools.AsyncExecutor)
	_, selfTimed := tool.(tools.SelfTimed)
	if !ok || defaultToolTimeout <= 0 || selfTimed || (isAsync && asyncCallback != nil) {
		return registry.ExecuteWithContext(ctx, name, args, channel, chatID, asyncCallback)
	}

	toolCtx, cancel := context.WithTimeout(ctx, defaultToolTimeout)
	defer cancel()
	done := make(chan *tools.ToolResult, 1)
	go func() {
		done <- registry.ExecuteWithContext(toolCtx, name, args, channel, chatID, asyncCallback)
	}()

	select {
	case result := <-done:
		return result
	case <-toolCtx.Done():
	}
	select {
	case result := <-done:
		return result
	case <-time.After(toolStopGrace):
	}

	if err := ctx.Err(); err != nil {
		return tools.ErrorResult(fmt.Sprintf("Tool %q was stopped.", name)).WithError(err)
	}
	err := fmt.Errorf("tool %q did not finish within %s", name, defaultToolTimeout)
	// The call may still run to its end; the model must not take the timeout
	// for proof that nothing happened and repeat a side effect.
	return tools.ErrorResult(fmt.Sprintf("Tool %q did not finish within %s and was abandoned. "+
		"It may still be running and may still complete: check its effects before calling it again.",
		name, defaultToolTimeout)).
		WithError(err)
}
