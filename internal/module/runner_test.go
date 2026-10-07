package module_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/internal/module"
	"github.com/xibodev/compa/v3/pkg/modproto"
)

// fakeModuleBinary is the fake module, built once for the whole test binary
// by TestMain.
var fakeModuleBinary string

// TestMain builds the fake module once.
//
// The tests run a REAL detached process rather than an in-process mock. That is
// the point: a mock would test the host's idea of a module, while this tests
// the boundary -- argv, stdout, stderr, exit codes, process trees, and the
// filesystem -- which is where every defect inherited from the donor lives.
//
// A build failure fails the run. Skipping would let every test that needs
// the module pass without having run.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "compa-fakemodule-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create build dir:", err)
		os.Exit(1)
	}
	fakeModuleBinary = filepath.Join(dir, "fakemodule")
	if runtime.GOOS == "windows" {
		fakeModuleBinary += ".exe"
	}
	cmd := exec.Command("go", "build", "-tags", "goolm,stdjson", "-o", fakeModuleBinary, "../../cmd/fakemodule")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build fakemodule: %v\n%s", err, out)
		os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// buildFakeModule returns the shared fake module binary.
func buildFakeModule(t *testing.T) string {
	t.Helper()
	return fakeModuleBinary
}

// copyFakeModule copies the fake module into a directory of its own, for a
// test that puts files beside the binary.
func copyFakeModule(t *testing.T) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), filepath.Base(fakeModuleBinary))
	in, err := os.Open(fakeModuleBinary)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return dest
}

func newRunner(t *testing.T) *module.Runner {
	return &module.Runner{Binary: buildFakeModule(t), ModuleID: "fake"}
}

func TestDescribeReturnsValidDescriptor(t *testing.T) {
	r := newRunner(t)

	d, res, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v (stderr: %s)", err, res.Stderr)
	}

	if d.Module != "fake" {
		t.Errorf("module = %q, want %q", d.Module, "fake")
	}
	if len(d.Capabilities) == 0 {
		t.Fatal("descriptor declares no capabilities")
	}
	// Discovery must be free and silent: the host may run it on startup or a
	// UI refresh, so a describe that costs money or chatters is a defect.
	if res.Stderr != "" {
		t.Errorf("describe wrote to stderr: %q", res.Stderr)
	}
	if res.Envelope.Execution.ActualCost == nil || *res.Envelope.Execution.ActualCost != 0 {
		t.Error("describe must report cost 0; it is genuinely free, not of unknown price")
	}
}

func TestInvokeRoundTrip(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	req := &modproto.Request{
		Capability: "fake.echo",
		Input:      json.RawMessage(`{"name":"ab","repeat":3}`),
	}
	res, err := r.Invoke(context.Background(), d, req)
	if err != nil {
		t.Fatalf("Invoke: %v (stderr: %s)", err, res.Stderr)
	}

	var out struct {
		Echoed string `json:"echoed"`
		Count  int    `json:"count"`
	}
	if err := json.Unmarshal(res.Envelope.Result, &out); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if out.Echoed != "ababab" {
		t.Errorf("echoed = %q, want %q", out.Echoed, "ababab")
	}

	// The host generated the ID and the module echoed it verbatim.
	if res.Envelope.RequestID != req.RequestID {
		t.Errorf("request_id = %q, want the host-generated %q", res.Envelope.RequestID, req.RequestID)
	}
	if !strings.HasPrefix(req.RequestID, "req_") {
		t.Errorf("host-generated request ID %q lacks the req_ prefix", req.RequestID)
	}

	// Diagnostics reached stderr and stayed out of the envelope.
	if !strings.Contains(res.Stderr, "fakemodule:") {
		t.Errorf("expected module diagnostics on stderr, got %q", res.Stderr)
	}
}

// TestStdoutPollutionIsRejected proves the host detects a module that prints
// anything to stdout besides its envelope.
//
// This is the donor's defect made impossible: it used CombinedOutput, so a
// module's own progress output landed in the stream being parsed as JSON.
func TestStdoutPollutionIsRejected(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	_, err = r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.stdout-pollution",
		Input:      json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("stdout pollution was accepted; exactly one JSON envelope is allowed on stdout")
	}
	if !strings.Contains(err.Error(), modproto.ErrHostProtocolViolation) &&
		!strings.Contains(err.Error(), modproto.ErrHostInvalidJSON) {
		t.Errorf("error should name a host protocol violation, got: %v", err)
	}
}

// TestOversizedOutputIsBounded proves host memory cannot be exhausted by a
// module that inlines a large payload instead of returning an artifact pointer.
func TestOversizedOutputIsBounded(t *testing.T) {
	r := newRunner(t)

	// Discovery must happen at the normal bound: the descriptor is legitimately
	// several KiB, and capping describe would fail this test before it reached
	// the capability under test.
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	// Now tighten the bound well below the module's 512 KiB inline blob.
	r.MaxOutputBytes = 4096

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.oversized",
		Input:      json.RawMessage(`{}`),
	})
	if err == nil {
		t.Fatal("oversized output was accepted; it must be bounded")
	}
	if !res.Truncated {
		t.Error("result should report truncation")
	}
	if !strings.Contains(err.Error(), modproto.ErrHostOutputTooLarge) {
		t.Errorf("error should name output_too_large, got: %v", err)
	}
	// The error must not carry the payload back into host logs or model
	// context, which is what the donor did.
	if len(err.Error()) > 500 {
		t.Errorf("error message is %d bytes; untrusted output must not be echoed back", len(err.Error()))
	}
}

// TestHangIsKilledOnDeadline proves a module that never returns cannot hang the
// host, and that the deadline is actually enforced rather than merely declared.
func TestHangIsKilledOnDeadline(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	started := time.Now()
	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.hang",
		Input:      json.RawMessage(`{}`),
		DeadlineMS: 1500,
	})
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("a hanging module was not killed")
	}
	if !strings.Contains(err.Error(), modproto.ErrHostTimeout) {
		t.Errorf("error should name a host timeout, got: %v", err)
	}
	// Generous upper bound: the point is that it returned at all, near the
	// deadline, rather than blocking indefinitely.
	if elapsed > 20*time.Second {
		t.Errorf("took %s to enforce a 1.5s deadline", elapsed)
	}
	if res == nil {
		t.Error("a timed-out invocation should still return diagnostics")
	}
}

// TestArtifactEscapingRootIsRejected proves the host re-validates every path a
// module returns instead of trusting the module's own confinement.
func TestArtifactEscapingRootIsRejected(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	workspace := t.TempDir()
	_, err = r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.escape-root",
		Input:      json.RawMessage(`{}`),
		Roots:      map[string]modproto.Root{"workspace": {Path: workspace, Mode: "rw"}},
	})
	if err == nil {
		t.Fatal("an artifact path escaping its root was accepted")
	}
	if !strings.Contains(err.Error(), modproto.ErrPathOutsideRoot) &&
		!strings.Contains(err.Error(), "escapes") &&
		!strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should name the confinement violation, got: %v", err)
	}
}

// TestUndeclaredEffectIsRejected proves the host compares what a module DID
// against what its capability DECLARED.
//
// This is the check that stops a capability declaring itself local and free,
// then reaching the network -- the shape that routes a paid or writing
// operation around approval.
func TestUndeclaredEffectIsRejected(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.undeclared-effect",
		Input:      json.RawMessage(`{}`),
	})
	// v1: effect over-reach warns rather than refuses, because the
	// declared/reported comparison is not yet unambiguous for capabilities that
	// describe a priced future call. The finding must still reach the host.
	if err != nil {
		t.Fatalf("effect over-reach must not fail the call in v1: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("a module reporting effects it never declared produced no warning")
	}
	joined := strings.Join(res.Warnings, " ")
	if !strings.Contains(joined, "network") && !strings.Contains(joined, "external_writes") {
		t.Errorf("warning should name the undeclared effect, got: %v", res.Warnings)
	}
}

// TestUnknownCostStaysUnknown is the money test: a capability declaring
// cost_known=false must report null, and the host must not turn that into zero
// anywhere along the path.
func TestUnknownCostStaysUnknown(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.estimate.unpriced",
		Input:      json.RawMessage(`{"name":"x"}`),
	})
	if err != nil {
		t.Fatalf("Invoke: %v (stderr: %s)", err, res.Stderr)
	}

	if res.Envelope.Execution.EstimatedCost != nil {
		t.Errorf("estimated_cost = %v, want null; the provider is unpriced, not free",
			*res.Envelope.Execution.EstimatedCost)
	}
	if res.Envelope.Execution.ActualCost != nil {
		t.Errorf("actual_cost = %v, want null", *res.Envelope.Execution.ActualCost)
	}
}

// TestWriteCapabilityUsesHostSuppliedRoot is the vertical proof's write half:
// a module resolves its output only through a root the host supplied, the file
// lands inside that root, and the returned artifact verifies.
func TestWriteCapabilityUsesHostSuppliedRoot(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	workspace := t.TempDir()
	req := &modproto.Request{
		Capability: "fake.report",
		Input:      json.RawMessage(`{"title":"Quarterly"}`),
		Roots:      map[string]modproto.Root{"workspace": {Path: workspace, Mode: "rw"}},
	}

	res, err := r.Invoke(context.Background(), d, req)
	if err != nil {
		t.Fatalf("Invoke: %v (stderr: %s)", err, res.Stderr)
	}

	if len(res.Envelope.Execution.Artifacts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(res.Envelope.Execution.Artifacts))
	}
	a := res.Envelope.Execution.Artifacts[0]

	if !res.Envelope.Execution.ExternalWrites {
		t.Error("a capability that wrote a file must report external_writes")
	}

	abs, err := module.ResolveArtifact(req, a)
	if err != nil {
		t.Fatalf("ResolveArtifact: %v", err)
	}
	body, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}

	// Provenance must actually verify, or the digest is decoration.
	if got := modproto.DigestSHA256(body); got != a.Digest {
		t.Errorf("digest mismatch:\n got %s\nwant %s", got, a.Digest)
	}
	if int64(len(body)) != a.Bytes {
		t.Errorf("bytes = %d, want %d", a.Bytes, len(body))
	}
	if !strings.HasPrefix(abs, filepath.Clean(workspace)) {
		t.Errorf("artifact resolved to %q, outside the supplied root %q", abs, workspace)
	}
}

// TestReadOnlyRootIsEnforced proves a read-only root is a real constraint, not
// a label. It matters because a staged cross-module artifact is supplied ro so
// a consumer cannot mutate the producer's output.
// TestReadOnlyRootIsHonouredByACooperativeModule checks that a module which
// respects the mode the host declares refuses to write into a read-only root.
//
// The name matters. This does NOT prove the host enforces read-only -- it
// cannot. A module runs with its working directory set to its own install
// directory and the OS permits writes there whatever mode the host declares;
// observed directly, a tool given a relative output path wrote a file into its
// own install root.
//
// The mode is a statement of intent to an honest module. What actually
// constrains a dishonest one is checked elsewhere: artifact paths are resolved
// against the roots actually supplied and refused when they escape, and
// declared content is digest-verified before it enters agent context. A test
// named "enforced" would invite the next reader to trust a boundary that is not
// there.
func TestReadOnlyRootIsHonouredByACooperativeModule(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	req := &modproto.Request{
		Capability: "fake.report",
		Input:      json.RawMessage(`{"title":"Nope"}`),
		Roots:      map[string]modproto.Root{"workspace": {Path: t.TempDir(), Mode: "ro"}},
	}
	res, err := r.Invoke(context.Background(), d, req)
	if err != nil {
		t.Fatalf("Invoke should return a structured module error, not a host error: %v", err)
	}
	if res.Envelope.OK {
		t.Fatal("writing to a read-only root must fail")
	}
	if res.Envelope.Error.Code != modproto.ErrPermissionDenied {
		t.Errorf("error code = %q, want %q", res.Envelope.Error.Code, modproto.ErrPermissionDenied)
	}
}

// TestMissingRootDeniesTheCapability proves the per-invocation authority model:
// a capability whose root was not supplied cannot proceed, so installing a
// module grants nothing by itself.
func TestMissingRootDeniesTheCapability(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.report",
		Input:      json.RawMessage(`{"title":"Nope"}`),
		// No roots at all.
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Envelope.OK {
		t.Fatal("a write capability with no supplied root must be denied")
	}
	if res.Envelope.Error.Code != modproto.ErrPermissionDenied {
		t.Errorf("error code = %q, want %q", res.Envelope.Error.Code, modproto.ErrPermissionDenied)
	}
}

// TestUnknownCapabilityIsAStructuredError proves a module reports an unknown
// capability with a stable code rather than crashing or improvising.
func TestUnknownCapabilityIsAStructuredError(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	// Declare it so the host's effects check has something to look up; the
	// module itself still rejects it.
	d.Capabilities = append(d.Capabilities, modproto.Capability{
		ID: "fake.nope", Title: "Nope", Summary: "Not implemented.",
		Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
	})

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.nope",
		Input:      json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if res.Envelope.OK {
		t.Fatal("unknown capability must fail")
	}
	if res.Envelope.Error.Code != modproto.ErrUnknownCapability {
		t.Errorf("error code = %q, want %q", res.Envelope.Error.Code, modproto.ErrUnknownCapability)
	}
}

// TestModuleInheritsNoEnvironment proves a module cannot pick up host
// credentials or resolve paths from the ambient environment, which is what
// makes "everything arrives in the request" enforceable.
//
// It asks the spawned process what it can see. An earlier version only checked
// that a canary VALUE was absent from stderr, which the module never printed,
// so it could not fail.
func TestModuleInheritsNoEnvironment(t *testing.T) {
	t.Setenv("COMPA_SECRET_CANARY", "must-not-leak")

	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.env",
		Input:      json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("a module must run correctly with no inherited environment: %v", err)
	}
	var seen struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(res.Envelope.Result, &seen); err != nil {
		t.Fatalf("result: %v", err)
	}
	if len(seen.Names) == 0 {
		t.Fatal("the module reported no environment at all, so this cannot tell" +
			" an empty environment from a broken probe")
	}
	for _, name := range seen.Names {
		if strings.EqualFold(name, "COMPA_SECRET_CANARY") {
			t.Fatal("host environment leaked into the module process")
		}
	}
}

// A cancelled invocation is the HOST's doing, not the module's.
//
// The process tree is killed correctly either way, but a killed process leaves
// truncated stdout -- so without an explicit cancellation check the decoder
// reports "stdout is not a single JSON envelope" and a module that behaved
// perfectly is recorded as having violated the protocol.
//
// That code is what tells an operator a module is misbehaving. Spending it on
// the host's own cancellation would teach them to ignore it.
func TestCancelledInvocationIsNotAProtocolViolation(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()

	_, err = r.Invoke(ctx, d, &modproto.Request{
		Capability: "fake.misbehave.hang",
		Input:      json.RawMessage(`{}`),
		Roots:      map[string]modproto.Root{"workspace": {Path: t.TempDir(), Mode: "rw"}},
		DeadlineMS: 60000,
	})
	if err == nil {
		t.Fatal("a cancelled invocation returned no error")
	}
	msg := err.Error()
	if strings.Contains(msg, modproto.ErrHostProtocolViolation) ||
		strings.Contains(msg, "not a single JSON envelope") {
		t.Fatalf("cancellation blamed on the module:\n%s", msg)
	}
	if !strings.Contains(msg, modproto.ErrCancelled) {
		t.Fatalf("cancellation not reported as cancelled:\n%s", msg)
	}
}

// The reported byte count is a claim, and it reaches the user: the artefact
// card prints it beside a download link, and it is what a person would check a
// transfer against. The stat that already proves the file exists carries the
// fact that settles it, so comparing costs nothing.
//
// Exercised through Invoke against the real fake module, which writes a genuine
// file and then misreports its size. Every other field is correct, so only the
// size comparison can refuse this.
func TestArtifactSizeMismatchIsRefused(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	_, err = r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.wrong-size",
		Input:      json.RawMessage(`{}`),
		Roots:      map[string]modproto.Root{"workspace": {Path: t.TempDir(), Mode: "rw"}},
		DeadlineMS: 15000,
	})

	if err == nil {
		t.Fatal("an artefact reporting the wrong size was accepted; the cockpit" +
			" would print that size beside a download link")
	}
	if !strings.Contains(err.Error(), "bytes but the file is") {
		t.Fatalf("refused for the wrong reason:\n%v", err)
	}
}

// The digest is the strongest claim a module makes and the one the cockpit
// presents as PROVENANCE -- the artefact card prints it under a comment saying
// it is what the host verified. It was not: verifyDigest existed but was
// reachable only from the CLI staging path, so every card served through the
// cockpit showed an unchecked number.
//
// Exercised through Invoke against a module that writes a real file, reports
// its true size, and lies only about the digest, so nothing but a re-hash can
// refuse it.
func TestArtifactDigestMismatchIsRefused(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	_, err = r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.misbehave.wrong-digest",
		Input:      json.RawMessage(`{}`),
		Roots:      map[string]modproto.Root{"workspace": {Path: t.TempDir(), Mode: "rw"}},
		DeadlineMS: 15000,
	})

	if err == nil {
		t.Fatal("an artefact with a false digest was accepted; the cockpit would" +
			" print that digest as provenance")
	}
	if !strings.Contains(err.Error(), "hashes to") {
		t.Fatalf("refused for the wrong reason:\n%v", err)
	}
}

// An honest artefact still passes: the check must not refuse the normal case,
// which is the failure that would look like the module being broken.
func TestHonestArtifactDigestPasses(t *testing.T) {
	r := newRunner(t)
	d, _, err := r.Describe(context.Background())
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}

	res, err := r.Invoke(context.Background(), d, &modproto.Request{
		Capability: "fake.report",
		Input:      json.RawMessage(`{"title":"ok"}`),
		Roots:      map[string]modproto.Root{"workspace": {Path: t.TempDir(), Mode: "rw"}},
		DeadlineMS: 15000,
	})
	if err != nil {
		t.Fatalf("an honest artefact was refused: %v", err)
	}
	if res == nil || res.Envelope == nil || !res.Envelope.OK {
		t.Fatalf("expected a successful envelope, got %+v", res)
	}
}
