// Command genfixtures writes the canonical host-side protocol fixtures.
//
// Fixtures are GENERATED rather than hand-written so they cannot drift from the
// types they claim to describe: a field renamed in pkg/modproto changes the
// fixture on the next run, and the diff is visible in review.
//
// Under the cross-checked-fixture model these files are the binding mechanism
// between the three lanes. Each lane parses the others' fixtures in its own
// tests, so drift breaks a test rather than surfacing in production.
//
// Regenerate with:
//
//	go run ./cmd/genfixtures
//
// Every path in these fixtures is fake and relative. No absolute host path,
// machine-specific value, or real credential may appear in a fixture.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/v4/pkg/modproto"
)

const fixtureDir = "testdata/fixtures"

// requestID is fixed rather than random so fixtures are byte-stable across
// runs; a regenerated fixture with no semantic change must produce no diff.
const requestID = "req_0000000000000000000000000000fake"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "genfixtures:", err)
		os.Exit(1)
	}
}

func run() error {
	if err := generate(fixtureDir); err != nil {
		return err
	}
	fmt.Println("fixtures written to", fixtureDir)
	return nil
}

// generate writes every fixture under root. Its output depends on nothing but
// this file and pkg/modproto, so the committed fixtures can be checked by
// regenerating them (TestCommittedFixturesAreCurrent).
func generate(root string) error {
	for _, f := range fixtures() {
		if err := writeFixture(root, f.dir, f.name, f.doc); err != nil {
			return err
		}
	}
	return writeMalformed(root)
}

type fixture struct {
	dir  string
	name string
	doc  any
}

func fixtures() []fixture {
	free := 0.0

	return []fixture{
		{"describe", "descriptor-full", mustDescribe(fullDescriptor())},
		{"describe", "descriptor-minimal", mustDescribe(minimalDescriptor())},

		{"invoke", "success-local-deterministic", mustResult(
			"fake", requestID,
			map[string]any{"echoed": "hello", "count": 3},
			modproto.Execution{
				Local: true, Provider: "local",
				EstimatedCost: &free, ActualCost: &free,
			})},

		{"invoke", "success-with-artifact", mustResult(
			"fake", requestID,
			map[string]any{"report": "generated"},
			modproto.Execution{
				Local: true, Provider: "local",
				EstimatedCost: &free, ActualCost: &free,
				Artifacts: []modproto.Artifact{{
					ID:        "artifact_report_1",
					Kind:      "fake.report/v1",
					Path:      "reports/report-1.json",
					Root:      "workspace",
					MediaType: "application/json",
					Bytes:     128,
					Digest:    modproto.DigestSHA256([]byte("fake report contents")),
					Title:     "Fake report",
				}},
			})},

		// The fixture every cost-rendering surface must be tested against.
		// estimated_cost is null because the provider is unpriced, NOT because
		// it is free. A cockpit that renders this as "$0.00" or "free" is
		// wrong, and this file is how that gets caught.
		{"invoke", "estimate-unknown-cost", mustResult(
			"fake", requestID,
			map[string]any{"will_call_provider": true, "provider": "unpriced-provider"},
			modproto.Execution{
				Network: true, Provider: "unpriced-provider",
				EstimatedCost: nil, ActualCost: nil,
			})},

		// A genuinely free local estimate: 0, not null. The pair with the
		// fixture above is the whole point of the pointer type.
		{"invoke", "estimate-known-free", mustResult(
			"fake", requestID,
			map[string]any{"will_call_provider": false},
			modproto.Execution{
				Local: true, Provider: "local",
				EstimatedCost: &free, ActualCost: &free,
			})},

		// Long-running: the handle-returning call. actual_cost MUST be null
		// here because the work has not run, and artifacts MUST be empty.
		{"invoke", "long-running-job-handle", mustResult(
			"fake", requestID,
			map[string]any{"job_id": "job_fake_0001", "state": "running"},
			modproto.Execution{
				Local: true, Provider: "local",
				EstimatedCost: &free, ActualCost: nil,
			})},

		{"invoke", "error-invalid-request", mustError(
			"fake", requestID,
			modproto.Error{
				Code:      modproto.ErrInvalidRequest,
				Message:   "field \"name\" is required",
				Retryable: false,
				Details:   map[string]any{"field": "name", "schema": "fake.echo.request/v1"},
			},
			modproto.Execution{Local: true, Provider: "local", EstimatedCost: &free, ActualCost: &free})},

		{"invoke", "error-unknown-capability", mustError(
			"fake", requestID,
			modproto.Error{
				Code:      modproto.ErrUnknownCapability,
				Message:   "unknown capability \"fake.nope\"",
				Retryable: false,
				Details:   map[string]any{"capability": "fake.nope"},
			},
			modproto.Execution{Local: true, Provider: "local", EstimatedCost: &free, ActualCost: &free})},

		{"invoke", "error-missing-requirement", mustError(
			"fake", requestID,
			modproto.Error{
				Code:      modproto.ErrMissingRequirement,
				Message:   "required binary \"ffmpeg\" was not found",
				Retryable: false,
				Details:   map[string]any{"kind": "binary", "name": "ffmpeg"},
			},
			modproto.Execution{Local: true, Provider: "local", EstimatedCost: &free, ActualCost: &free})},

		{"invoke", "error-permission-denied", mustError(
			"fake", requestID,
			modproto.Error{
				Code:      modproto.ErrPermissionDenied,
				Message:   "capability requires paid provider authority, which was not granted for this invocation",
				Retryable: false,
				Details:   map[string]any{"required": "paid_providers", "provider": "unpriced-provider"},
			},
			modproto.Execution{Local: true, Provider: "", EstimatedCost: nil, ActualCost: nil})},

		{"invoke", "error-path-outside-root", mustError(
			"fake", requestID,
			modproto.Error{
				Code:      modproto.ErrPathOutsideRoot,
				Message:   "requested path escapes its declared root",
				Retryable: false,
				Details:   map[string]any{"root": "workspace", "path": "../../etc/passwd"},
			},
			modproto.Execution{Local: true, Provider: "local", EstimatedCost: &free, ActualCost: &free})},

		// A failed PAID call. The critical property: it failed, but cost is
		// unknown rather than zero, because a provider may have billed before
		// failing. Reporting 0 here would under-report real spend.
		{"invoke", "error-provider-failure-unknown-cost", mustError(
			"fake", requestID,
			modproto.Error{
				Code:      modproto.ErrProviderFailure,
				Message:   "provider returned 503 after accepting the request",
				Retryable: true,
				Details:   map[string]any{"status": 503, "provider": "unpriced-provider"},
			},
			modproto.Execution{Network: true, Provider: "unpriced-provider", EstimatedCost: nil, ActualCost: nil})},

		{"invoke", "request-example", exampleRequest()},
	}
}

func exampleRequest() modproto.Request {
	r := modproto.Request{
		Protocol:   modproto.ProtocolID,
		Capability: "fake.echo",
		RequestID:  requestID,
		Input:      json.RawMessage(`{"name":"hello","repeat":3}`),
		Roots: map[string]modproto.Root{
			// Canonicalized absolute paths are host-supplied. These are
			// deliberately fake POSIX-style paths so the fixture is portable.
			"workspace": {Path: "/fake/state/workspace", Mode: "rw"},
			"seed_in":   {Path: "/fake/state/artifacts/seed-0001", Mode: "ro"},
		},
		Grants:         modproto.Grants{},
		DeadlineMS:     30000,
		MaxOutputBytes: 1 << 20,
	}
	r.Normalize()
	return r
}

func fullDescriptor() *modproto.Descriptor {
	overlay := []byte("# Fake module overlay\n\nGuidance the host may compose into the agent's prompt.\n")
	skill := []byte("# Fake echo skill\n\nHow to use fake.echo well.\n")

	return &modproto.Descriptor{
		Module:           "fake",
		Name:             "Fake Module",
		Version:          "1.0.0",
		ProtocolVersions: []string{modproto.ProtocolID},
		Capabilities: []modproto.Capability{
			{
				ID:              "fake.echo",
				Title:           "Echo",
				Summary:         "Echo structured input back, deterministically.",
				RequestSchema:   "fake.echo.request/v1",
				ResultSchema:    "fake.echo.result/v1",
				ArtifactSchemas: []string{},
				Effects: modproto.Effects{
					Local: true, Provider: "local", CostKnown: true,
				},
				Skills: []string{"fake.echo-usage"},
			},
			{
				ID:              "fake.report",
				Title:           "Report",
				Summary:         "Write a deterministic report artifact into the workspace root.",
				RequestSchema:   "fake.report.request/v1",
				ResultSchema:    "fake.report.result/v1",
				ArtifactSchemas: []string{"fake.report/v1"},
				Effects: modproto.Effects{
					Local: true, ExternalWrites: true, Provider: "local", CostKnown: true,
				},
				Skills: []string{},
			},
			{
				ID:              "fake.slow",
				Title:           "Slow job",
				Summary:         "Start a long-running job and return a handle to poll.",
				RequestSchema:   "fake.slow.request/v1",
				ResultSchema:    "fake.slow.result/v1",
				ArtifactSchemas: []string{},
				Effects: modproto.Effects{
					Local: true, Provider: "local", CostKnown: true,
				},
				Skills:         []string{},
				LongRunning:    true,
				PollCapability: "fake.jobs.status",
			},
			{
				ID:              "fake.jobs.status",
				Title:           "Job status",
				Summary:         "Poll a job handle for completion, cost, and artifacts.",
				RequestSchema:   "fake.jobs.status.request/v1",
				ResultSchema:    "fake.jobs.status.result/v1",
				ArtifactSchemas: []string{},
				Effects: modproto.Effects{
					Local: true, Provider: "local", CostKnown: true,
				},
				Skills: []string{},
			},
		},
		RequestSchemas: map[string]json.RawMessage{
			"fake.echo.request/v1":        objectSchema(`{"name":{"type":"string"},"repeat":{"type":"integer"}}`, `["name"]`),
			"fake.report.request/v1":      objectSchema(`{"title":{"type":"string"}}`, `["title"]`),
			"fake.slow.request/v1":        objectSchema(`{"duration_ms":{"type":"integer"}}`, `[]`),
			"fake.jobs.status.request/v1": objectSchema(`{"job_id":{"type":"string"}}`, `["job_id"]`),
		},
		ResultSchemas: map[string]json.RawMessage{
			"fake.echo.result/v1":        objectSchema(`{"echoed":{"type":"string"},"count":{"type":"integer"}}`, `["echoed"]`),
			"fake.report.result/v1":      objectSchema(`{"report":{"type":"string"}}`, `["report"]`),
			"fake.slow.result/v1":        objectSchema(`{"job_id":{"type":"string"},"state":{"type":"string"}}`, `["job_id"]`),
			"fake.jobs.status.result/v1": objectSchema(`{"job_id":{"type":"string"},"state":{"type":"string"}}`, `["job_id","state"]`),
		},
		ArtifactSchemas: map[string]json.RawMessage{
			"fake.report/v1": objectSchema(`{"title":{"type":"string"},"rows":{"type":"array"}}`, `["title"]`),
		},
		AgentOverlays: []modproto.Overlay{{
			ID:     "fake.overlay",
			Title:  "Fake module guidance",
			Path:   "agents/fake.md",
			Digest: modproto.DigestSHA256(overlay),
			Tokens: 120,
		}},
		Skills: []modproto.Skill{{
			ID:      "fake.echo-usage",
			Title:   "Using fake.echo",
			Summary: "When and how to call fake.echo.",
			Path:    "skills/echo-usage/SKILL.md",
			Digest:  modproto.DigestSHA256(skill),
			Tokens:  240,
		}},
		Permissions: modproto.Permissions{
			FilesystemRead:  []string{"seed_in"},
			FilesystemWrite: []string{"workspace"},
		},
		Requirements: []modproto.Requirement{{
			Kind:      "binary",
			Name:      "ffmpeg",
			Available: false,
			// Detail is what turns "Needs setup" into something actionable,
			// so the fixture models a useful one rather than an empty string.
			Detail: "ffmpeg was not found on PATH; install it to enable fake.report",
		}},
	}
}

// minimalDescriptor is the smallest legal descriptor: a module with one
// capability and nothing optional. It exists so every lane can prove that
// empty collections serialize as [] and {} rather than null.
func minimalDescriptor() *modproto.Descriptor {
	return &modproto.Descriptor{
		Module:           "fake",
		Name:             "Fake Module",
		Version:          "1.0.0",
		ProtocolVersions: []string{modproto.ProtocolID},
		Capabilities: []modproto.Capability{{
			ID:      "fake.echo",
			Title:   "Echo",
			Summary: "Echo structured input back, deterministically.",
			Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
		}},
	}
}

func objectSchema(properties, required string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":%s,"required":%s,"additionalProperties":false}`,
		properties, required))
}

func mustDescribe(d *modproto.Descriptor) modproto.Envelope {
	env, err := modproto.NewDescribeEnvelope(d)
	if err != nil {
		panic(err)
	}
	return env
}

func mustResult(module, reqID string, payload any, exec modproto.Execution) modproto.Envelope {
	env, err := modproto.NewResultEnvelope(module, "invoke", reqID, payload, exec)
	if err != nil {
		panic(err)
	}
	return env
}

func mustError(module, reqID string, e modproto.Error, exec modproto.Execution) modproto.Envelope {
	return modproto.NewErrorEnvelope(module, "invoke", reqID, e, exec)
}

// writeMalformed writes the NEGATIVE fixtures: documents the host must REJECT.
//
// These are raw bytes rather than marshalled structs, because the whole point
// is that they cannot be produced by the correct types. They are what proves
// the host validates rather than trusts.
func writeMalformed(root string) error {
	cases := []struct {
		name  string
		bytes string
		why   string
	}{
		{"two-json-documents", `{"protocol":"xibodev.module/v1","ok":true}` + "\n" + `{"second":"document"}`,
			"stdout must carry exactly one JSON envelope"},
		{"progress-line-before-envelope", `reading transcript 1...` + "\n" + `{"protocol":"xibodev.module/v1","ok":true}`,
			"progress belongs on stderr, never stdout"},
		{"trailing-log-line", `{"protocol":"xibodev.module/v1","ok":true}` + "\n" + `done in 1.2s`,
			"trailing output after the envelope is a protocol violation"},
		{"empty-stdout", ``,
			"a module must always emit an envelope, even on failure"},
		{"not-json", `<html><body>error</body></html>`,
			"stdout must be JSON"},
		{"null-collections", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"result":{},"warnings":null,"execution":{"local":true,"network":false,"external_writes":false,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":null}}`,
			"warnings and artifacts must be [] rather than null"},
		{"cost-zero-instead-of-unknown", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"result":{},"warnings":[],"execution":{"local":false,"network":true,"external_writes":false,"provider":"unpriced-provider","estimated_cost":0,"actual_cost":0,"artifacts":[]}}`,
			"an unpriced provider call reporting 0 rather than null under-reports real spend; not structurally detectable, caught by comparing against declared Effects.CostKnown"},
		{"wrong-protocol", `{"protocol":"some.other/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"result":{},"warnings":[],"execution":{"local":true,"network":false,"external_writes":false,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[]}}`,
			"the host refuses a protocol it does not speak"},
		{"request-id-not-echoed", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"req_invented_by_module","ok":true,"result":{},"warnings":[],"execution":{"local":true,"network":false,"external_writes":false,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[]}}`,
			"request_id must be echoed verbatim so the host can correlate"},
		{"ok-true-with-error", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"result":{},"error":{"code":"internal","message":"boom","retryable":false,"details":{}},"warnings":[],"execution":{"local":true,"network":false,"external_writes":false,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[]}}`,
			"exactly one of result/error, matching ok"},
		{"absolute-artifact-path", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"result":{},"warnings":[],"execution":{"local":true,"network":false,"external_writes":true,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[{"id":"a1","kind":"fake.report/v1","path":"/etc/passwd","root":"workspace","media_type":"application/json","bytes":1,"digest":"sha256:0000000000000000000000000000000000000000000000000000000000000000"}]}}`,
			"artifact paths are relative to a declared root; an absolute path makes confinement uncheckable"},
		{"bare-hex-digest", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"result":{},"warnings":[],"execution":{"local":true,"network":false,"external_writes":true,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[{"id":"a1","kind":"fake.report/v1","path":"r.json","root":"workspace","media_type":"application/json","bytes":1,"digest":"0000000000000000000000000000000000000000000000000000000000000000"}]}}`,
			"digests carry a mandatory sha256: prefix"},
		{"ok-true-with-neither-result-nor-error", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"warnings":[],"execution":{"local":true,"network":false,"external_writes":false,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[]}}`,
			"ok:true must carry a result; the empty case is the one a real module hits by forgetting to set it, and it decodes to a typed zero value rather than an obvious error"},
		{"operation-is-capability-not-verb", `{"protocol":"xibodev.module/v1","module":"fake","operation":"fake.echo","request_id":"` + requestID + `","ok":true,"result":{},"warnings":[],"execution":{"local":true,"network":false,"external_writes":false,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[]}}`,
			"operation names the VERB (describe|invoke), never the capability; the capability travels in the request and is correlated by request_id"},
		{"oversized-inline-payload", `{"protocol":"xibodev.module/v1","module":"fake","operation":"invoke","request_id":"` + requestID + `","ok":true,"result":{"inline_blob":"` + strings.Repeat("A", 4096) + `"},"warnings":[],"execution":{"local":true,"network":false,"external_writes":false,"provider":"local","estimated_cost":0,"actual_cost":0,"artifacts":[]}}`,
			"a large payload inlined in result instead of returned as an artifact pointer; NOT structurally invalid, so it is enforced by max_output_bytes at read time rather than by the validator -- this fixture exists to test that bound"},
	}

	dir := filepath.Join(root, "malformed")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	readme := "# Malformed fixtures\n\nEvery file here is a document the host MUST REJECT.\n\nThey are raw bytes rather than marshalled structs, because the point is that\nthe correct types cannot produce them. They are how each lane proves it\nvalidates module output rather than trusting it.\n\n| File | Why it must be rejected |\n| --- | --- |\n"

	for _, c := range cases {
		name := c.name + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(c.bytes), 0o644); err != nil {
			return err
		}
		readme += fmt.Sprintf("| `%s` | %s |\n", name, c.why)
	}

	return os.WriteFile(filepath.Join(dir, "README.md"), []byte(readme), 0o644)
}

func writeFixture(root, dir, name string, doc any) error {
	full := filepath.Join(root, dir)
	if err := os.MkdirAll(full, 0o755); err != nil {
		return err
	}

	blob, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	blob = append(blob, '\n')

	return os.WriteFile(filepath.Join(full, name+".json"), blob, 0o644)
}
