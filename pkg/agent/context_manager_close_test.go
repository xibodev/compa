//go:build !mipsle && !netbsd && !(freebsd && arm)

package agent

import (
	"context"
	"testing"
)

// closeRecorder is a ContextManager that also holds a resource, like the
// seahorse one holds an open SQLite handle.
type closeRecorder struct{ closed bool }

func (c *closeRecorder) Assemble(context.Context, *AssembleRequest) (*AssembleResponse, error) {
	return nil, nil
}
func (c *closeRecorder) Compact(context.Context, *CompactRequest) error { return nil }
func (c *closeRecorder) Ingest(context.Context, *IngestRequest) error   { return nil }
func (c *closeRecorder) Clear(context.Context, string) error            { return nil }
func (c *closeRecorder) Close() error                                   { c.closed = true; return nil }

// The bug this exists for: AgentLoop.Close released the MCP manager, the
// evolution bridge, the registry, the hooks and the event bus -- and left the
// context manager open. The seahorse one owns a SQLite connection, so every
// shutdown leaked a database handle.
//
// It was visible the whole time and read as someone else's problem: four tests
// failed on Windows with "the process cannot access the file because it is
// being used by another process" during TempDir cleanup, with every assertion
// in them PASSING. Windows refuses to unlink an open file; Linux does not, so
// the same leak there produces a green run.
func TestCloseReleasesTheContextManager(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()

	rec := &closeRecorder{}
	al.contextManager = rec

	al.Close()

	if !rec.closed {
		t.Fatal("Close left the context manager open, so its database handle leaks")
	}
}

// A manager with nothing to release must not be a crash on the way down: the
// default one holds no resource and implements no Close.
func TestCloseToleratesAManagerWithNoResources(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	al.contextManager = &defaultContextManager{al: al}

	al.Close() // must not panic
}

// Close is called after Stop on a loop that may never have started, so a nil
// manager must be survivable too.
func TestCloseToleratesNoContextManager(t *testing.T) {
	al, _, _, _, cleanup := newTestAgentLoop(t)
	defer cleanup()
	al.contextManager = nil

	al.Close() // must not panic
}
