package modproto_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

// fixtureRoot is relative to this package; fixtures live at the repo root so
// every lane can find them by a stable path.
const fixtureRoot = "../../testdata/fixtures"

// TestValidFixturesValidate proves every positive fixture is actually legal
// under the host's own validator.
//
// This is the file the other two lanes point their tests at. Under the
// cross-checked-fixture model these bytes are the contract, so a fixture that
// does not validate is a broken contract rather than a broken test.
func TestValidFixturesValidate(t *testing.T) {
	t.Run("describe", func(t *testing.T) {
		for _, name := range fixtureNames(t, "describe") {
			t.Run(name, func(t *testing.T) {
				blob := readFixture(t, "describe", name)

				env, err := modproto.DecodeEnvelope(blob)
				if err != nil {
					t.Fatalf("DecodeEnvelope: %v", err)
				}
				// describe has no request, so no request ID to echo.
				if err := modproto.ValidateEnvelope(env, "describe", ""); err != nil {
					t.Fatalf("ValidateEnvelope: %v", err)
				}

				var d modproto.Descriptor
				if err := json.Unmarshal(env.Result, &d); err != nil {
					t.Fatalf("decode descriptor: %v", err)
				}
				if err := modproto.ValidateDescriptor(&d); err != nil {
					t.Fatalf("ValidateDescriptor: %v", err)
				}
			})
		}
	})

	t.Run("invoke", func(t *testing.T) {
		for _, name := range fixtureNames(t, "invoke") {
			if name == "request-example" {
				continue // a Request, not an Envelope; checked separately
			}
			t.Run(name, func(t *testing.T) {
				blob := readFixture(t, "invoke", name)

				env, err := modproto.DecodeEnvelope(blob)
				if err != nil {
					t.Fatalf("DecodeEnvelope: %v", err)
				}
				if err := modproto.ValidateEnvelope(env, "invoke", fixtureRequestID); err != nil {
					t.Fatalf("ValidateEnvelope: %v", err)
				}
			})
		}
	})
}

const fixtureRequestID = "req_0000000000000000000000000000fake"

// TestMalformedFixturesAreRejected is the negative half, and the more important
// one: it proves the host actually validates rather than merely documenting
// that it should.
//
// Each case names the specific violation so a regression says what stopped
// being caught.
func TestMalformedFixturesAreRejected(t *testing.T) {
	// Cases the DECODER must reject: stdout purity violations.
	decodeRejects := map[string]string{
		"two-json-documents.txt":            "two documents on stdout",
		"progress-line-before-envelope.txt": "progress printed to stdout",
		"trailing-log-line.txt":             "log line after the envelope",
		"empty-stdout.txt":                  "no envelope at all",
		"not-json.txt":                      "stdout is not JSON",
	}
	for name, why := range decodeRejects {
		t.Run(name, func(t *testing.T) {
			blob := readMalformed(t, name)
			if _, err := modproto.DecodeEnvelope(blob); err == nil {
				t.Fatalf("DecodeEnvelope accepted %s (%s), want rejection", name, why)
			}
		})
	}

	// Cases the VALIDATOR must reject: the bytes parse, but the contract is
	// violated. These are the dangerous ones, because they look fine.
	validateRejects := map[string]string{
		"null-collections.txt":                      "warnings and artifacts were null",
		"wrong-protocol.txt":                        "unsupported protocol id",
		"request-id-not-echoed.txt":                 "module invented its own request_id",
		"ok-true-with-error.txt":                    "both result and error present",
		"absolute-artifact-path.txt":                "artifact path was absolute",
		"bare-hex-digest.txt":                       "digest missing the sha256: prefix",
		"ok-true-with-neither-result-nor-error.txt": "ok:true with no result at all",
		"operation-is-capability-not-verb.txt":      "operation carried a capability ID instead of a verb",
	}
	for name, why := range validateRejects {
		t.Run(name, func(t *testing.T) {
			blob := readMalformed(t, name)
			env, err := modproto.DecodeEnvelope(blob)
			if err != nil {
				t.Fatalf("fixture should parse but fail validation; decode failed: %v", err)
			}
			if err := modproto.ValidateEnvelope(env, "invoke", fixtureRequestID); err == nil {
				t.Fatalf("ValidateEnvelope accepted %s (%s), want rejection", name, why)
			}
		})
	}
}

// TestUnknownCostFixtureIsNotZero guards the fixture that every cost-rendering
// surface must be tested against.
//
// A cockpit that renders this as "free" or "$0.00" is wrong in the direction
// that costs real money: the provider is unpriced, not free. Asserting it here
// means the fixture cannot quietly acquire a zero.
func TestUnknownCostFixtureIsNotZero(t *testing.T) {
	blob := readFixture(t, "invoke", "estimate-unknown-cost")

	// Assert on the raw bytes, not just the decoded struct: the wire form is
	// what the other lanes parse.
	if !strings.Contains(string(blob), `"estimated_cost": null`) {
		t.Error("estimate-unknown-cost must carry estimated_cost null on the wire")
	}
	if !strings.Contains(string(blob), `"actual_cost": null`) {
		t.Error("estimate-unknown-cost must carry actual_cost null on the wire")
	}

	env, err := modproto.DecodeEnvelope(blob)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Execution.EstimatedCost != nil {
		t.Errorf("estimated_cost decoded to %v, want nil (unknown)", *env.Execution.EstimatedCost)
	}

	// And its counterpart must be a real zero, or the pair proves nothing.
	freeBlob := readFixture(t, "invoke", "estimate-known-free")
	freeEnv, err := modproto.DecodeEnvelope(freeBlob)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if freeEnv.Execution.EstimatedCost == nil {
		t.Fatal("estimate-known-free must report 0, not null")
	}
	if *freeEnv.Execution.EstimatedCost != 0 {
		t.Errorf("estimate-known-free estimated_cost = %v, want 0", *freeEnv.Execution.EstimatedCost)
	}
}

// TestJobHandleReportsProvisionalExecution guards the rule that the
// handle-returning call must not claim a cost or artifacts for work that has
// not happened yet.
func TestJobHandleReportsProvisionalExecution(t *testing.T) {
	env, err := modproto.DecodeEnvelope(readFixture(t, "invoke", "long-running-job-handle"))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.Execution.ActualCost != nil {
		t.Errorf("actual_cost = %v on a job handle; the work has not run, so it must be null", *env.Execution.ActualCost)
	}
	if len(env.Execution.Artifacts) != 0 {
		t.Errorf("job handle reported %d artifacts; the work has not run, so it must be empty", len(env.Execution.Artifacts))
	}
}

// TestFailedPaidCallDoesNotReportZeroCost guards the case most likely to
// under-report real spend: a provider that billed and then failed.
func TestFailedPaidCallDoesNotReportZeroCost(t *testing.T) {
	env, err := modproto.DecodeEnvelope(readFixture(t, "invoke", "error-provider-failure-unknown-cost"))
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if env.OK {
		t.Error("fixture must represent a failure")
	}
	if env.Execution.ActualCost != nil {
		t.Errorf("actual_cost = %v after a provider failure; it must stay unknown, since the provider may have billed before failing", *env.Execution.ActualCost)
	}
}

// TestFixturesContainNoAbsolutePaths keeps fixtures portable and free of
// machine-specific values, so they mean the same thing in every lane.
func TestFixturesContainNoAbsolutePaths(t *testing.T) {
	// The request fixture legitimately carries host-supplied absolute roots;
	// those are fake POSIX paths by construction and are checked separately.
	for _, dir := range []string{"describe", "invoke"} {
		for _, name := range fixtureNames(t, dir) {
			if name == "request-example" {
				continue
			}
			blob := string(readFixture(t, dir, name))
			for _, bad := range []string{`"C:\\`, `"/home/`, `"/Users/`, `"E:\\`, `"D:\\`} {
				if strings.Contains(blob, bad) {
					t.Errorf("%s/%s contains a machine-specific path fragment %q", dir, name, bad)
				}
			}
		}
	}
}

// TestRequestFixtureIsWellFormed checks the host-authored side of the wire.
func TestRequestFixtureIsWellFormed(t *testing.T) {
	var r modproto.Request
	if err := json.Unmarshal(readFixture(t, "invoke", "request-example"), &r); err != nil {
		t.Fatalf("decode request: %v", err)
	}

	if r.Protocol != modproto.ProtocolID {
		t.Errorf("protocol = %q, want %q", r.Protocol, modproto.ProtocolID)
	}
	if r.RequestID == "" {
		t.Error("request_id must be host-generated and non-empty")
	}
	// Bounds must be present, or "bounded envelope" is aspirational.
	if r.DeadlineMS <= 0 {
		t.Error("deadline_ms must be set; an unbounded invocation cannot be cancelled on schedule")
	}
	if r.MaxOutputBytes <= 0 {
		t.Error("max_output_bytes must be set; an unbounded stdout can exhaust host memory")
	}

	for name, root := range r.Roots {
		if root.Mode != "ro" && root.Mode != "rw" {
			t.Errorf("root %q has mode %q, want \"ro\" or \"rw\"", name, root.Mode)
		}
		if root.Path == "" {
			t.Errorf("root %q has an empty path", name)
		}
	}
	if _, ok := r.Roots["seed_in"]; ok {
		if r.Roots["seed_in"].Mode != "ro" {
			t.Error("a staged seed root must be read-only, so a consumer cannot mutate the producer's output")
		}
	}
}

func fixtureNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(fixtureRoot, dir))
	if err != nil {
		t.Fatalf("read fixture dir %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	if len(names) == 0 {
		t.Fatalf("no fixtures in %s; run: go run ./cmd/genfixtures", dir)
	}
	return names
}

func readFixture(t *testing.T, dir, name string) []byte {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(fixtureRoot, dir, name+".json"))
	if err != nil {
		t.Fatalf("read fixture %s/%s: %v", dir, name, err)
	}
	return blob
}

func readMalformed(t *testing.T, name string) []byte {
	t.Helper()
	blob, err := os.ReadFile(filepath.Join(fixtureRoot, "malformed", name))
	if err != nil {
		t.Fatalf("read malformed fixture %s: %v", name, err)
	}
	return blob
}

// TestOperationMustBeAVerbNotACapability pins the VALUE of a shared field, not
// just its presence.
//
// A module lane independently implemented `operation` as the capability ID
// while this lane implemented it as the verb. Both were self-consistent and
// both passed every fixture, because no fixture asserted what the field should
// CONTAIN. A shared field with no fixture pinning its value is exactly where
// two lanes silently diverge, so this test pins it from both directions.
func TestOperationMustBeAVerbNotACapability(t *testing.T) {
	// A module answering with a capability ID in `operation` is rejected.
	blob := readMalformed(t, "operation-is-capability-not-verb.txt")
	env, err := modproto.DecodeEnvelope(blob)
	if err != nil {
		t.Fatalf("fixture should parse: %v", err)
	}
	if err := modproto.ValidateEnvelope(env, modproto.OperationInvoke, fixtureRequestID); err == nil {
		t.Error("accepted a capability ID in operation; it must be a verb")
	}

	// And the host asking for something that is not a verb is rejected too,
	// so a caller's typo cannot quietly redefine the contract by matching.
	good := readFixture(t, "invoke", "success-local-deterministic")
	goodEnv, err := modproto.DecodeEnvelope(good)
	if err != nil {
		t.Fatalf("DecodeEnvelope: %v", err)
	}
	if err := modproto.ValidateEnvelope(goodEnv, "fake.echo", fixtureRequestID); err == nil {
		t.Error("accepted a non-verb expectation; only describe and invoke are protocol verbs")
	}

	// Every positive fixture must carry one of exactly two values.
	for _, dir := range []string{"describe", "invoke"} {
		for _, name := range fixtureNames(t, dir) {
			if name == "request-example" {
				continue
			}
			e, err := modproto.DecodeEnvelope(readFixture(t, dir, name))
			if err != nil {
				t.Fatalf("%s/%s: %v", dir, name, err)
			}
			if e.Operation != modproto.OperationDescribe && e.Operation != modproto.OperationInvoke {
				t.Errorf("%s/%s has operation %q, want %q or %q",
					dir, name, e.Operation, modproto.OperationDescribe, modproto.OperationInvoke)
			}
		}
	}
}

// TestOversizedPayloadIsBoundedNotValidated records an honest limit: a module
// that inlines a large payload instead of returning an artifact pointer is not
// STRUCTURALLY invalid, so the validator cannot catch it. The bound is
// max_output_bytes, enforced when reading stdout.
//
// The fixture and this test exist so that limit is explicit rather than an
// assumption, and so the read-time bound has something to be tested against.
func TestOversizedPayloadIsBoundedNotValidated(t *testing.T) {
	blob := readMalformed(t, "oversized-inline-payload.txt")

	env, err := modproto.DecodeEnvelope(blob)
	if err != nil {
		t.Fatalf("fixture should parse; it is well-formed, merely oversized: %v", err)
	}
	if err := modproto.ValidateEnvelope(env, modproto.OperationInvoke, fixtureRequestID); err != nil {
		t.Fatalf("oversized payload must pass STRUCTURAL validation; it is caught by max_output_bytes, not by the validator: %v", err)
	}

	// The property that actually matters: it exceeds a realistic bound, so a
	// read-time cap has something to catch.
	const smallBound = 1024
	if len(blob) <= smallBound {
		t.Errorf("fixture is %d bytes, too small to exercise an output bound", len(blob))
	}
	if len(env.Execution.Artifacts) != 0 {
		t.Error("the fixture models the WRONG behaviour: a large payload inlined rather than returned as an artifact pointer")
	}
}

// TestCostZeroInsteadOfUnknownIsCaughtByDeclaredEffects closes the loop on the
// most expensive fixture in the set.
//
// The envelope alone cannot reveal this violation: 0 is a valid number, so a
// structural validator must pass it. Only the capability's declared
// CostKnown=false shows that the module claimed knowledge it does not have.
//
// The lookup is done against the DESCRIPTOR rather than a caller-supplied flag,
// which matters: a validator that accepts the declaration as an argument merely
// ratifies whatever the caller believed, turning a wrong belief into a passing
// test. That is the same shape as a validator accepting any verb a caller
// invents, which is a real bug this suite already caught.
func TestCostZeroInsteadOfUnknownIsCaughtByDeclaredEffects(t *testing.T) {
	blob := readMalformed(t, "cost-zero-instead-of-unknown.txt")

	env, err := modproto.DecodeEnvelope(blob)
	if err != nil {
		t.Fatalf("fixture should parse: %v", err)
	}
	// Structural validation MUST pass: this is the honest limit.
	if err := modproto.ValidateEnvelope(env, modproto.OperationInvoke, fixtureRequestID); err != nil {
		t.Fatalf("structural validation must pass an unpriced-reporting-zero envelope; it is caught only against declared effects: %v", err)
	}

	// A descriptor whose capability declares its cost is NOT known.
	d := &modproto.Descriptor{
		Module:           "fake",
		Name:             "Fake Module",
		Version:          "1.0.0",
		ProtocolVersions: []string{modproto.ProtocolID},
		Capabilities: []modproto.Capability{{
			ID:      "fake.paid",
			Title:   "Paid",
			Summary: "Calls an unpriced provider.",
			Effects: modproto.Effects{Network: true, Provider: "unpriced-provider", CostKnown: false},
		}},
	}
	d.Normalize()

	warnings, err := modproto.CheckExecutionAgainstDeclared(d, "fake.paid", &env.Execution)
	if err != nil {
		t.Fatalf("v1 surfaces this as a warning rather than a refusal: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("an unpriced capability reporting cost 0 must at least warn")
	}
	joined := warnings[0].Error()
	for _, w := range warnings {
		joined += " " + w.Error()
	}
	if !strings.Contains(joined, "actual_cost") {
		t.Errorf("warning should name the offending field, got: %v", warnings)
	}

	// The same envelope against a capability that genuinely knows its cost is
	// fine, or the check would just be a blanket ban on zero.
	dFree := &modproto.Descriptor{
		Module: "fake", Name: "Fake", Version: "1.0.0",
		ProtocolVersions: []string{modproto.ProtocolID},
		Capabilities: []modproto.Capability{{
			ID: "fake.free", Title: "Free", Summary: "Local and free.",
			Effects: modproto.Effects{Network: true, Provider: "unpriced-provider", CostKnown: true},
		}},
	}
	dFree.Normalize()
	if w, err := modproto.CheckExecutionAgainstDeclared(dFree, "fake.free", &env.Execution); err != nil || len(w) > 0 {
		t.Errorf("a capability that knows its cost may report 0: err=%v warnings=%v", err, w)
	}
}

// TestDeclaredEffectsCatchOverreach covers the other half: a capability that
// declared less than it did. Under-promising is allowed; over-reaching is not.
func TestDeclaredEffectsCatchOverreach(t *testing.T) {
	d := &modproto.Descriptor{
		Module: "fake", Name: "Fake", Version: "1.0.0",
		ProtocolVersions: []string{modproto.ProtocolID},
		Capabilities: []modproto.Capability{{
			ID: "fake.quiet", Title: "Quiet", Summary: "Local, no writes, no network.",
			Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
		}},
	}
	d.Normalize()

	free := 0.0

	// Effect over-reach is WARNING-class in v1 rather than a refusal: running
	// the real modules showed the declared/reported comparison is not yet
	// unambiguous, since a capability may be describing the run it priced
	// rather than the call it just made. The finding must still be RAISED --
	// silently dropping it would remove the signal approval routing depends on.
	warnings, err := modproto.CheckExecutionAgainstDeclared(d, "fake.quiet", &modproto.Execution{
		Network: true, EstimatedCost: &free, ActualCost: &free,
	})
	if err != nil {
		t.Errorf("effect over-reach must not fail the call in v1: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("a capability declaring no network that reports network use must at least warn")
	}

	// Declared no external writes, reported writes. This is the one that
	// routes around approval, so it must always be surfaced.
	warnings, err = modproto.CheckExecutionAgainstDeclared(d, "fake.quiet", &modproto.Execution{
		ExternalWrites: true, EstimatedCost: &free, ActualCost: &free,
	})
	if err != nil {
		t.Errorf("effect over-reach must not fail the call in v1: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("a capability declaring no external writes that reports writes must at least warn")
	}

	// Under-promising is fine: declared local, did nothing surprising.
	if err := modproto.ValidateExecutionAgainstDeclared(d, "fake.quiet", &modproto.Execution{
		Local: true, EstimatedCost: &free, ActualCost: &free,
	}); err != nil {
		t.Errorf("an invocation that stayed within its declaration must pass: %v", err)
	}

	// An undeclared capability has no approval basis at all.
	if err := modproto.ValidateExecutionAgainstDeclared(d, "fake.undeclared", &modproto.Execution{}); err == nil {
		t.Error("execution of an undeclared capability must be rejected, not silently allowed")
	}
}

// TestUnpricedCapabilityMayReportZeroForALocalCall records a distinction that
// only surfaced when a real rendering module ran.
//
// creative.tools.estimate declares cost_known=false -- the RUN it prices is
// unpriced -- yet the estimate call itself is local, touches no network, and is
// free by design. Reporting 0 there is honest. Execution describes what THIS
// call did; Effects.CostKnown describes the operation the capability performs.
// Conflating them rejected a correct module for being truthful.
func TestUnpricedCapabilityMayReportZeroForALocalCall(t *testing.T) {
	d := &modproto.Descriptor{
		Module: "fake", Name: "Fake", Version: "1.0.0",
		ProtocolVersions: []string{modproto.ProtocolID},
		Capabilities: []modproto.Capability{{
			ID: "fake.estimate", Title: "Estimate", Summary: "Price an unpriced run.",
			Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: false},
		}},
	}
	d.Normalize()

	free := 0.0

	// Local and free: legal, because no provider was reached.
	if w, err := modproto.CheckExecutionAgainstDeclared(d, "fake.estimate", &modproto.Execution{
		Local: true, Provider: "local", EstimatedCost: &free, ActualCost: &free,
	}); err != nil || len(w) > 0 {
		t.Errorf("a local, non-billing call under an unpriced capability may report 0: err=%v warnings=%v", err, w)
	}

	// Reached a provider and still claims 0: warned, since that is where money
	// is actually spent.
	w, err := modproto.CheckExecutionAgainstDeclared(d, "fake.estimate", &modproto.Execution{
		Network: true, Provider: "unpriced-provider", EstimatedCost: &free, ActualCost: &free,
	})
	if err != nil {
		t.Errorf("v1 warns rather than refuses: %v", err)
	}
	if len(w) == 0 {
		t.Error("a call that reported reaching an unpriced provider while claiming cost 0 must warn")
	}
}
