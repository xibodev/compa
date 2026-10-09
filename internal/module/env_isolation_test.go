package module_test

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v4/internal/module"
	"github.com/xibodev/compa/v4/pkg/modproto"
)

// The environment guarantee, proven against a REAL SPAWNED PROCESS rather than
// against the helper that builds the list.
//
// tempOnlyEnv() has good tests, but they assert what the function RETURNS. The
// guarantee the README makes is about what a module can SEE, and those are only
// the same while cmd.Env is assigned exactly once and never appended to. A
// single `cmd.Env = append(os.Environ(), ...)` anywhere would satisfy every
// existing test and leak the host's entire environment.
//
// This matters concretely: ELEVENLABS_API_KEY is set on the machine this was
// developed on, and a module that inherited it could reach a paid provider
// without ever being granted that credential.
func TestASpawnedModuleSeesNoneOfTheHostEnvironment(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "host-secret-must-not-reach-a-module")
	t.Setenv("COMPA_TEST_CANARY", "host-secret-must-not-reach-a-module")

	r := &module.Runner{Binary: buildFakeModule(t), ModuleID: "fake"}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	d, _, err := r.Describe(ctx)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}

	res, err := r.Invoke(ctx, d, &modproto.Request{
		Capability: "fake.env",
		Input:      json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}

	var out struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(res.Envelope.Result, &out); err != nil {
		t.Fatalf("result: %v", err)
	}

	for _, name := range out.Names {
		switch strings.ToUpper(name) {
		case "TEMP", "TMP", "TMPDIR":
			// The deliberate exception: os.MkdirTemp fails with no environment
			// at all, which surfaced as "unable to create staging directory"
			// and read like a permissions problem.
		case "SYSTEMROOT", "COMSPEC", "PATHEXT", "PROMPT":
			// Injected by Go's os/exec on Windows regardless of cmd.Env --
			// verified with a standalone probe, since reading the code would
			// not have shown it. Many Windows programs will not start without
			// SYSTEMROOT, so this is a platform floor rather than a leak.
			//
			// It carries no secret and the host cannot suppress it. Naming the
			// four keeps the assertion strict: anything ELSE appearing here is
			// a real leak, and this list is short enough that a fifth entry
			// would have to be justified rather than absorbed.
			if runtime.GOOS != "windows" {
				t.Errorf("%q appeared outside Windows, where nothing injects it", name)
			}
		default:
			t.Errorf("the spawned module can see %q, which the host never"+
				" granted it", name)
		}
	}

	if len(out.Names) == 0 {
		t.Error("the module saw NO environment at all; staging will fail with" +
			" something that reads like a permissions problem")
	}
}
