package module_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/modproto"
)

// installedModule returns a runner for a module installed under the host's
// state root, or skips.
//
// These tests exercise the boundary against REAL third-party module binaries
// rather than the fake one. They run only when the environment names one, and
// skip otherwise, because the host must build and test standalone:
//
//	COMPA_TEST_MODULES        module binaries to describe, separated by the
//	                          OS path list separator
//	COMPA_TEST_PROBE_MODULE   a module binary that runs a declared subprocess
//	COMPA_TEST_PROBE_REQUEST  the request that makes it do so, as JSON
//	                          {"capability": ..., "input": ..., "extra": {...}};
//	                          "$MEDIA" inside a JSON string is replaced by the
//	                          path of a small WAV file the test writes
func realModule(t *testing.T, bin string) *module.Runner {
	t.Helper()

	abs, err := filepath.Abs(bin)
	if err != nil {
		t.Skipf("resolve %s: %v", bin, err)
	}
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("module binary %s does not exist", abs)
	}
	return &module.Runner{Binary: abs}
}

// TestRealModuleDescribesItself proves a third-party module's descriptor passes
// host validation -- the cross-implementation check running against a live
// binary rather than a checked-in fixture.
func TestRealModuleDescribesItself(t *testing.T) {
	bins := filepath.SplitList(os.Getenv("COMPA_TEST_MODULES"))
	if len(bins) == 0 {
		t.Skip("COMPA_TEST_MODULES names no module binary")
	}
	for _, bin := range bins {
		t.Run(filepath.Base(bin), func(t *testing.T) {
			r := realModule(t, bin)

			d, res, err := r.Describe(context.Background())
			if err != nil {
				t.Fatalf("Describe: %v (stderr: %s)", err, res.Stderr)
			}
			if d.Module == "" || len(d.Capabilities) == 0 {
				t.Fatalf("descriptor is empty: module=%q capabilities=%d", d.Module, len(d.Capabilities))
			}
			// Discovery must stay cheap and silent: the host may run it on
			// startup or a UI refresh.
			if res.Stderr != "" {
				t.Errorf("describe wrote to stderr: %q", res.Stderr)
			}
		})
	}
}

// TestGrantedBinaryIsLoadBearing is the security proof for the subprocess
// authority model, run against a real module that genuinely shells out.
//
// Modules inherit no environment, so there is no PATH to search. The host
// resolves each DECLARED binary to an absolute path and supplies it per
// invocation. This asserts both halves: with the grant the call succeeds, and
// WITHOUT it the module fails closed rather than finding the binary another
// way. The negative half is what makes the grant load-bearing rather than
// decorative -- without it, a module could quietly restore ambient authority
// and every "enforcement" here would be theatre.
func TestGrantedBinaryIsLoadBearing(t *testing.T) {
	bin, rawRequest := os.Getenv("COMPA_TEST_PROBE_MODULE"), os.Getenv("COMPA_TEST_PROBE_REQUEST")
	if bin == "" || rawRequest == "" {
		t.Skip("COMPA_TEST_PROBE_MODULE and COMPA_TEST_PROBE_REQUEST name no probe")
	}
	r := realModule(t, bin)

	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	r.ModuleID = d.Module

	// A real media file, so the tool does actual work rather than failing on
	// its input before it ever reaches the binary.
	media := filepath.Join(t.TempDir(), "tone.wav")
	if err := writeSilentWAV(media); err != nil {
		t.Fatalf("write test media: %v", err)
	}
	quoted := mustJSON(media)
	rawRequest = strings.ReplaceAll(rawRequest, "$MEDIA", quoted[1:len(quoted)-1])
	var probe struct {
		Capability string                     `json:"capability"`
		Input      json.RawMessage            `json:"input"`
		Extra      map[string]json.RawMessage `json:"extra"`
	}
	if err := json.Unmarshal([]byte(rawRequest), &probe); err != nil || probe.Capability == "" {
		t.Fatalf("COMPA_TEST_PROBE_REQUEST must be a JSON object naming a capability: %v", err)
	}

	newRequest := func() *modproto.Request {
		return &modproto.Request{
			Capability: probe.Capability,
			Input:      probe.Input,
			Extra:      probe.Extra,
			DeadlineMS: 60000,
		}
	}

	// WITH the grant: the module resolves its binary from the supplied path.
	granted := newRequest()
	if missing := module.GrantBinaries(d, granted); len(missing) > 0 {
		t.Skipf("host could not resolve %v; the tool under test is unavailable here", missing)
	}
	res, err := r.Invoke(context.Background(), d, granted)
	if err != nil {
		t.Fatalf("Invoke with grant: %v (stderr: %s)", err, res.Stderr)
	}
	if !res.Envelope.OK {
		t.Fatalf("granted invocation failed: %s: %s",
			res.Envelope.Error.Code, res.Envelope.Error.Message)
	}

	// WITHOUT it: the module must fail closed, not fall back to a lookup.
	denied := newRequest()
	denied.Binaries = map[string]string{}
	res, err = r.Invoke(context.Background(), d, denied)
	if err != nil {
		t.Fatalf("Invoke without grant should return a structured module error: %v", err)
	}
	if res.Envelope.OK {
		t.Fatal("the module ran a subprocess it was never granted; the binary grant is not load-bearing")
	}
	if res.Envelope.Error.Code != "dependency_missing" {
		t.Errorf("error code = %q, want dependency_missing", res.Envelope.Error.Code)
	}
}

func mustJSON(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// writeSilentWAV writes a minimal valid WAV so a probe tool has real media to
// inspect, without depending on ffmpeg being available to generate it.
func writeSilentWAV(path string) error {
	const samples = 1024
	data := make([]byte, 0, 44+samples*2)

	le32 := func(v uint32) []byte {
		return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
	}
	le16 := func(v uint16) []byte { return []byte{byte(v), byte(v >> 8)} }

	data = append(data, "RIFF"...)
	data = append(data, le32(36+samples*2)...)
	data = append(data, "WAVEfmt "...)
	data = append(data, le32(16)...)
	data = append(data, le16(1)...)     // PCM
	data = append(data, le16(1)...)     // mono
	data = append(data, le32(44100)...) // sample rate
	data = append(data, le32(88200)...) // byte rate
	data = append(data, le16(2)...)     // block align
	data = append(data, le16(16)...)    // bits per sample
	data = append(data, "data"...)
	data = append(data, le32(samples*2)...)
	data = append(data, make([]byte, samples*2)...)

	return os.WriteFile(path, data, 0o644)
}
