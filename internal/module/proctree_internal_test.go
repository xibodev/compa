package module

import (
	"errors"
	"os"
	"testing"
)

// A stop asked for before the process is known signals nothing. On Windows a
// console break to process group 0 would reach every process on the console,
// this host included.
func TestInterruptBeforeTheProcessIsKnownSignalsNothing(t *testing.T) {
	if err := newProcessTree().interrupt(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("interrupt() = %v, want os.ErrProcessDone", err)
	}
}
