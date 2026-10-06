package module_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/internal/module"
)

// A truncated descriptor must be REFUSED, not parsed. A partial JSON document
// cannot be trusted even when it happens to parse -- a capability list cut in
// half would silently install a module with fewer abilities than it declared,
// and nobody would be told.
//
// This also pins the answer I gave a module author about whether describe is
// bounded. I answered it from memory and got it wrong; a test is what makes the
// answer checkable rather than remembered.
func TestATruncatedDescriptorIsRefusedNotParsed(t *testing.T) {
	r := &module.Runner{
		Binary:         buildFakeModule(t),
		MaxOutputBytes: 64, // far below any real descriptor
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d, res, err := r.Describe(ctx)

	if err == nil {
		t.Fatal("a descriptor larger than the cap was accepted; the host would" +
			" install a module from a partial document")
	}
	if d != nil {
		t.Fatal("a descriptor was returned alongside the refusal")
	}
	if res == nil || !res.Truncated {
		t.Fatalf("truncation was not reported, so the reason is invisible: %+v", res)
	}
	if !strings.Contains(err.Error(), "stdout") {
		t.Fatalf("the refusal does not say what happened: %v", err)
	}
}

// The same descriptor under the cap must load normally -- a bound that refuses
// everything is not a bound.
func TestADescriptorUnderTheCapLoads(t *testing.T) {
	r := &module.Runner{Binary: buildFakeModule(t)} // default 1 MiB

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	d, _, err := r.Describe(ctx)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if d == nil || len(d.Capabilities) == 0 {
		t.Fatal("the descriptor loaded with no capabilities")
	}
}
