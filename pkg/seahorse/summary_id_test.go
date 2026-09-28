package seahorse

import (
	"testing"
	"time"
)

// The bug this exists for: generateSummaryID accepted the summary's content and
// ignored it, deriving the ID from the clock alone. Windows ticks about every
// 15ms, so a loop creating several summaries gives them all the SAME id and
// every one after the first is refused by a UNIQUE constraint.
//
// It surfaced as a test failure that had been written off as "pre-existing",
// which is what a real bug looks like when nobody reads the message: the
// summaries were being DROPPED, and the only visible symptom was one red test.
func TestDistinctSummariesGetDistinctIDs(t *testing.T) {
	now := time.Now().UTC()

	first := generateSummaryID("First summary about topics A and B", now)
	second := generateSummaryID("Second summary about topics C and D", now)

	if first == second {
		t.Fatalf("two different summaries share an id (%s), so the second is"+
			" refused and lost", first)
	}
}

// The clock does not separate summaries on Windows, and identical content does
// not either -- a compaction loop can legitimately write the same text twice.
// Uniqueness cannot depend on either input being different.
//
// This is the case the first fix still failed: hashing the content fixed
// different summaries at one instant, and left identical ones colliding.
func TestIdenticalSummariesAtOneInstantStillDiffer(t *testing.T) {
	now := time.Now().UTC()

	if a, b := generateSummaryID("same", now), generateSummaryID("same", now); a == b {
		t.Fatalf("two inserts share id %s, so the second is refused and lost", a)
	}
}

// Uniqueness must hold across a burst, not just a pair: the real caller writes
// CondensedMinFanout summaries in a loop with no delay.
func TestABurstOfSummariesAreAllDistinct(t *testing.T) {
	now := time.Now().UTC()

	seen := make(map[string]bool, 64)
	for i := 0; i < 64; i++ {
		id := generateSummaryID("leaf summary content", now)
		if seen[id] {
			t.Fatalf("id %s repeated within one burst at iteration %d", id, i)
		}
		seen[id] = true
	}
}
