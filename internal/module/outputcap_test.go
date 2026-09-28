package module

import (
	"bytes"
	"strings"
	"testing"
)

// The output cap is what stops a module pushing unbounded data into host
// memory. It had no test, so nothing established that it stops anything.
//
// It must also keep ACCEPTING writes past the limit rather than erroring: a
// write error makes the child fail in a confusing way, and the host has already
// decided the output is unusable. So "capped" here means the host stores no
// more, not that the child is interrupted.
func TestOutputCapStopsStoringPastTheLimit(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, limit: 10}

	n, err := lw.Write([]byte(strings.Repeat("a", 100)))

	if err != nil {
		t.Fatalf("the writer errored instead of discarding: %v", err)
	}
	if n != 100 {
		t.Fatalf("reported %d of 100 bytes written; a short count makes the"+
			" child fail confusingly", n)
	}
	if buf.Len() != 10 {
		t.Fatalf("stored %d bytes past a limit of 10", buf.Len())
	}
	if !lw.truncated {
		t.Fatal("truncation was not recorded, so the host cannot refuse the" +
			" result and will parse a partial document")
	}
}

// Output UNDER the limit must pass through untouched and unflagged -- a cap
// that truncates everything is not a cap, and a false truncation flag makes the
// host refuse a perfectly good result.
func TestOutputUnderTheCapIsUntouched(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, limit: 100}

	if _, err := lw.Write([]byte("small")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if got := buf.String(); got != "small" {
		t.Fatalf("output was altered: %q", got)
	}
	if lw.truncated {
		t.Fatal("a result within the cap was flagged truncated, so the host" +
			" would refuse it")
	}
}

// A write landing exactly on the limit is not truncation: an off-by-one here
// refuses a result that fits.
func TestAWriteExactlyOnTheLimitIsNotTruncation(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, limit: 5}

	if _, err := lw.Write([]byte("12345")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if lw.truncated {
		t.Fatal("output that exactly fits was flagged truncated")
	}
	if buf.String() != "12345" {
		t.Fatalf("stored %q, want the whole write", buf.String())
	}
}

// The limit spans writes: a module emitting many small chunks must be capped
// the same as one emitting a single large one.
func TestTheCapSpansSuccessiveWrites(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, limit: 10}

	for i := 0; i < 20; i++ {
		if _, err := lw.Write([]byte("xxxxx")); err != nil {
			t.Fatalf("Write %d: %v", i, err)
		}
	}

	if buf.Len() != 10 {
		t.Fatalf("stored %d bytes across many writes past a limit of 10", buf.Len())
	}
	if !lw.truncated {
		t.Fatal("many small writes escaped the cap's truncation flag")
	}
}

// A zero-length write is not truncation. A module may flush an empty buffer,
// and flagging that as truncated makes the host refuse a complete result.
//
// This is the one case the early-return guard changes: at the limit it flags
// ANY write, including an empty one. Without the guard the remaining logic
// takes the same branch for every non-empty write, so the guard's only distinct
// effect is this bug.
func TestAnEmptyWriteAtTheLimitIsNotTruncation(t *testing.T) {
	var buf bytes.Buffer
	lw := &limitedWriter{w: &buf, limit: 5}

	if _, err := lw.Write([]byte("12345")); err != nil { // exactly fills it
		t.Fatalf("Write: %v", err)
	}
	if _, err := lw.Write(nil); err != nil { // adds nothing
		t.Fatalf("Write(nil): %v", err)
	}

	if lw.truncated {
		t.Fatal("an empty write was recorded as truncation, so the host would" +
			" refuse a complete result")
	}
}
