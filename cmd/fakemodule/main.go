// Command fakemodule is a deterministic module used to test the host's module
// boundary without involving a real module, a network, or a paid provider.
//
// It is a REAL detached module: the host discovers and executes it exactly as
// it would any other, over the same two verbs and the same wire contract. That
// is the point -- a mock inside the host would test the host's idea of a
// module, whereas this tests the boundary itself.
//
// It also deliberately misbehaves on demand. Capabilities under fake.misbehave
// produce the protocol violations the host must survive: stdout pollution,
// oversized output, a stdout flood, a hung process, a process that ignores
// termination, a grandchild left running, a path escaping its root. Those
// paths exist so the host's bounds are proven against a real process rather
// than a fixture.
//
//	fakemodule module describe --json
//	fakemodule module invoke <capability> --input <request.json>
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xibodev/compa/v3/pkg/modproto"
)

const moduleID = "fake"

func main() {
	code := run(os.Args[1:])
	os.Exit(code)
}

func run(args []string) int {
	// A helper process some misbehaving capabilities start: it holds the
	// inherited stdout and sleeps, like an ffmpeg left behind by a module. It
	// ignores the polite request to stop, so only a kill that reaches the
	// whole process tree ends it.
	if len(args) == 2 && args[0] == "grandchild" {
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		seconds, _ := strconv.Atoi(args[1])
		time.Sleep(time.Duration(seconds) * time.Second)
		return 0
	}

	// The host speaks exactly two verbs. Anything else is not a protocol call,
	// so it fails as a plain CLI error rather than as an envelope.
	if len(args) < 2 || args[0] != "module" {
		fmt.Fprintln(os.Stderr, "usage: fakemodule module describe --json | fakemodule module invoke <capability> --input <file>")
		return 2
	}

	switch args[1] {
	case modproto.OperationDescribe:
		return emit(describe())
	case modproto.OperationInvoke:
		if len(args) < 5 || args[3] != "--input" {
			fmt.Fprintln(os.Stderr, "usage: fakemodule module invoke <capability> --input <file>")
			return 2
		}
		return emit(invoke(args[2], args[4]))
	default:
		fmt.Fprintf(os.Stderr, "unknown module operation %q\n", args[1])
		return 2
	}
}

// emit writes exactly one JSON envelope to stdout and nothing else.
//
// Exit code mirrors ok, so a host can fail fast on a non-zero exit without
// parsing -- but the envelope remains the authority, since a module may fail
// while still reporting cost and artifacts honestly.
func emit(env modproto.Envelope) int {
	blob, err := json.Marshal(env)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		return 1
	}
	os.Stdout.Write(blob)
	os.Stdout.Write([]byte("\n"))
	if env.OK {
		return 0
	}
	return 1
}

func describe() modproto.Envelope {
	env, err := modproto.NewDescribeEnvelope(descriptor())
	if err == nil {
		env.Result, err = withContractVersion(env.Result)
	}
	if err != nil {
		return modproto.NewErrorEnvelope(moduleID, modproto.OperationDescribe, "",
			modproto.Error{Code: modproto.ErrInternal, Message: err.Error()}, modproto.Execution{Local: true})
	}
	return env
}

// withContractVersion adds the contract_version a test asks for, through a
// file named contract_version beside the binary (modules run with no
// environment). Without the file the descriptor is a plain v1 one.
func withContractVersion(result json.RawMessage) (json.RawMessage, error) {
	self, err := os.Executable()
	if err != nil {
		return result, nil
	}
	version, err := os.ReadFile(filepath.Join(filepath.Dir(self), "contract_version"))
	if err != nil {
		return result, nil
	}
	var fields map[string]any
	if err := json.Unmarshal(result, &fields); err != nil {
		return nil, err
	}
	fields["contract_version"] = strings.TrimSpace(string(version))
	return json.Marshal(fields)
}

func invoke(capabilityID, inputPath string) modproto.Envelope {
	free := 0.0
	localFree := modproto.Execution{
		Local: true, Provider: "local",
		EstimatedCost: &free, ActualCost: &free,
	}

	fail := func(reqID, code, msg string, details map[string]any) modproto.Envelope {
		return modproto.NewErrorEnvelope(moduleID, modproto.OperationInvoke, reqID,
			modproto.Error{Code: code, Message: msg, Details: details}, localFree)
	}

	raw, err := os.ReadFile(inputPath)
	if err != nil {
		return fail("", modproto.ErrInvalidRequest, "could not read the request file", map[string]any{"path": filepath.Base(inputPath)})
	}

	var req modproto.Request
	if err := json.Unmarshal(raw, &req); err != nil {
		return fail("", modproto.ErrInvalidRequest, "request file is not valid JSON", nil)
	}

	// A module must refuse a protocol it does not speak rather than guessing.
	if req.Protocol != modproto.ProtocolID {
		return fail(req.RequestID, modproto.ErrUnsupportedProtocol,
			fmt.Sprintf("this module speaks %q, not %q", modproto.ProtocolID, req.Protocol), nil)
	}
	// The capability on the command line and in the request must agree, or the
	// host cannot trust which one was authorized.
	if req.Capability != capabilityID {
		return fail(req.RequestID, modproto.ErrInvalidRequest,
			fmt.Sprintf("capability %q on the command line does not match %q in the request", capabilityID, req.Capability), nil)
	}

	// Diagnostics go to stderr, never stdout. The host captures and bounds
	// this, surfaces it as diagnostics, and never parses it.
	fmt.Fprintf(os.Stderr, "fakemodule: handling %s (request %s)\n", capabilityID, req.RequestID)

	switch capabilityID {
	case "fake.env":
		// Reports the environment variable NAMES this process can see, so the
		// host's "no inherited environment" guarantee can be tested against a
		// REAL SPAWNED PROCESS rather than against the helper that builds the
		// list. Names only, never values: a test fixture that printed secrets
		// would be a worse leak than the one it guards.
		names := make([]string, 0, 8)
		for _, entry := range os.Environ() {
			if name, _, found := strings.Cut(entry, "="); found {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"names": names}, localFree)
		return env

	case "fake.where":
		// Reports the working directory the host started this process in.
		wd, err := os.Getwd()
		if err != nil {
			return fail(req.RequestID, modproto.ErrInternal, err.Error(), nil)
		}
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"cwd": wd}, localFree)
		return env

	case "fake.echo":
		var in struct {
			Name   string `json:"name"`
			Repeat int    `json:"repeat"`
		}
		if err := json.Unmarshal(req.Input, &in); err != nil {
			return fail(req.RequestID, modproto.ErrInvalidRequest, "input is not valid JSON", nil)
		}
		if in.Name == "" {
			return fail(req.RequestID, modproto.ErrInvalidRequest, `field "name" is required`,
				map[string]any{"field": "name"})
		}
		if in.Repeat <= 0 {
			in.Repeat = 1
		}
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"echoed": strings.Repeat(in.Name, in.Repeat), "count": in.Repeat}, localFree)
		return env

	case "fake.report":
		return writeReport(req, localFree)

	case "fake.estimate.unpriced":
		// The cost-honesty case: this capability declares cost_known=false, so
		// it MUST report null rather than 0. Reporting 0 here would be a lie
		// the host's declared-effects check is designed to catch.
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"will_call_provider": true, "provider": "unpriced-provider"},
			modproto.Execution{Network: true, Provider: "unpriced-provider", EstimatedCost: nil, ActualCost: nil})
		return env

	case "fake.misbehave.stdout-pollution":
		// Printed BEFORE the envelope, which is what makes it a violation.
		fmt.Println("progress: 50%")

	case "fake.misbehave.oversized":
		// Inlines a large payload instead of returning an artifact pointer.
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"blob": strings.Repeat("A", 512*1024)}, localFree)
		return env

	case "fake.misbehave.hang":
		// Never returns. The host must kill it on deadline.
		//
		// time.Sleep rather than `select {}`: an empty select is detected by
		// the Go runtime as a deadlock, which exits the process immediately.
		// That would have made this capability terminate on its own and the
		// host's timeout path would never have been exercised -- the test
		// would have passed for the wrong reason.
		time.Sleep(24 * time.Hour)

	case "fake.misbehave.ignore-termination":
		// Ignores the polite request to stop, so only the host's escalation
		// to a forced kill ends it. The marker in the workspace carries its
		// process ID and says it is running, so a test knows the request to
		// stop arrived after this point.
		signal.Ignore(os.Interrupt, syscall.SIGTERM)
		writeMarker(req, "ignoring-termination", strconv.Itoa(os.Getpid()))
		time.Sleep(24 * time.Hour)

	case "fake.misbehave.note-termination":
		// Stops when asked, and records that it was asked, so a test can tell
		// a graceful stop from a forced one.
		stopped := make(chan os.Signal, 1)
		signal.Notify(stopped, os.Interrupt, syscall.SIGTERM)
		writeMarker(req, "waiting-for-termination", "ready")
		<-stopped
		writeMarker(req, "terminated", "stopped by signal")
		return fail(req.RequestID, modproto.ErrCancelled, "stopped on request", nil)

	case "fake.misbehave.grandchild":
		// Starts a helper that outlives any deadline and holds stdout, then
		// hangs. Killing only this process would leave the helper running,
		// holding the output pipe and the invocation with it.
		if err := startGrandchild(req, 120); err != nil {
			return fail(req.RequestID, modproto.ErrInternal, err.Error(), nil)
		}
		time.Sleep(24 * time.Hour)

	case "fake.misbehave.orphan-stdout":
		// Starts a helper that holds stdout, then answers and exits normally.
		// The host must not wait for the helper to finish before returning.
		if err := startGrandchild(req, 30); err != nil {
			return fail(req.RequestID, modproto.ErrInternal, err.Error(), nil)
		}
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"ok": true}, localFree)
		return env

	case "fake.misbehave.flood":
		// Writes to stdout without end. The host must stop it once the output
		// bound is reached rather than wait for the deadline.
		chunk := []byte(strings.Repeat("A", 64<<10))
		for {
			if _, err := os.Stdout.Write(chunk); err != nil {
				return fail(req.RequestID, modproto.ErrInternal, "stdout closed", nil)
			}
		}

	case "fake.misbehave.escape-root":
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"wrote": "outside"},
			modproto.Execution{
				Local: true, ExternalWrites: true, Provider: "local",
				EstimatedCost: &free, ActualCost: &free,
				Artifacts: []modproto.Artifact{{
					ID: "escape", Kind: "fake.report/v1",
					Path: "../../escaped.json", Root: "workspace",
					MediaType: "application/json", Bytes: 1,
					Digest: modproto.DigestSHA256([]byte("x")),
				}},
			})
		return env

	case "fake.misbehave.wrong-size":
		// Writes a real file inside a granted root, then reports a byte count
		// that does not match it. Every other field is correct, so only the
		// size comparison can catch this.
		body := []byte("the actual contents of this artifact")
		if root, ok := req.Roots["workspace"]; ok {
			_ = os.WriteFile(filepath.Join(root.Path, "sized.bin"), body, 0o644)
		}
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"wrote": "sized.bin"},
			modproto.Execution{
				Local: true, ExternalWrites: true, Provider: "local",
				EstimatedCost: &free, ActualCost: &free,
				Artifacts: []modproto.Artifact{{
					ID: "sized", Kind: "fake.report/v1",
					Path: "sized.bin", Root: "workspace",
					MediaType: "application/octet-stream",
					Bytes:     1, // the file is far larger
					Digest:    modproto.DigestSHA256(body),
				}},
			})
		return env

	case "fake.misbehave.wrong-digest":
		// Writes a real file, reports its true size, and lies about the digest.
		// Only a re-hash can catch this -- which is the point, since the card
		// presents that digest as provenance.
		body := []byte("contents whose digest will be misreported")
		if root, ok := req.Roots["workspace"]; ok {
			_ = os.WriteFile(filepath.Join(root.Path, "digested.bin"), body, 0o644)
		}
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"wrote": "digested.bin"},
			modproto.Execution{
				Local: true, ExternalWrites: true, Provider: "local",
				EstimatedCost: &free, ActualCost: &free,
				Artifacts: []modproto.Artifact{{
					ID: "digested", Kind: "fake.report/v1",
					Path: "digested.bin", Root: "workspace",
					MediaType: "application/octet-stream",
					Bytes:     int64(len(body)),
					Digest:    modproto.DigestSHA256([]byte("different content entirely")),
				}},
			})
		return env

	case "fake.misbehave.undeclared-effect":
		// Declares local-and-free in the descriptor, then reports network use
		// and an unknown cost. The host must reject this rather than trust it.
		env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			map[string]any{"did": "something unexpected"},
			modproto.Execution{Network: true, ExternalWrites: true, Provider: "surprise", EstimatedCost: nil, ActualCost: nil})
		return env

	default:
		return fail(req.RequestID, modproto.ErrUnknownCapability,
			fmt.Sprintf("unknown capability %q", capabilityID), map[string]any{"capability": capabilityID})
	}

	// Reached only by stdout-pollution, which emits a valid envelope AFTER
	// having already polluted stdout.
	env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
		map[string]any{"ok": true}, localFree)
	return env
}

// writeReport exercises the honest path for a capability that writes: it
// resolves its output ONLY through a host-supplied root, never from the
// environment or the working directory.
func writeReport(req modproto.Request, exec modproto.Execution) modproto.Envelope {
	root, ok := req.Roots["workspace"]
	if !ok {
		return modproto.NewErrorEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			modproto.Error{
				Code:    modproto.ErrPermissionDenied,
				Message: `capability requires the "workspace" root, which was not supplied for this invocation`,
				Details: map[string]any{"required_root": "workspace"},
			}, exec)
	}
	if root.Mode != "rw" {
		return modproto.NewErrorEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			modproto.Error{
				Code:    modproto.ErrPermissionDenied,
				Message: `the "workspace" root was supplied read-only`,
				Details: map[string]any{"root": "workspace", "mode": root.Mode},
			}, exec)
	}

	var in struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(req.Input, &in)
	if in.Title == "" {
		in.Title = "Untitled"
	}

	body, err := json.MarshalIndent(map[string]any{"title": in.Title, "rows": []string{"a", "b"}}, "", "  ")
	if err != nil {
		return modproto.NewErrorEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			modproto.Error{Code: modproto.ErrInternal, Message: err.Error()}, exec)
	}

	rel := "reports/report-1.json"
	abs := filepath.Join(root.Path, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return modproto.NewErrorEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			modproto.Error{Code: modproto.ErrInternal, Message: "could not create the report directory"}, exec)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		return modproto.NewErrorEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
			modproto.Error{Code: modproto.ErrInternal, Message: "could not write the report"}, exec)
	}

	exec.ExternalWrites = true
	exec.Artifacts = []modproto.Artifact{{
		ID:        "artifact_report_1",
		Kind:      "fake.report/v1",
		Path:      rel, // relative to its root, never absolute
		Root:      "workspace",
		MediaType: "application/json",
		Bytes:     int64(len(body)),
		Digest:    modproto.DigestSHA256(body),
		Title:     in.Title,
	}}

	env, _ := modproto.NewResultEnvelope(moduleID, modproto.OperationInvoke, req.RequestID,
		map[string]any{"report": rel}, exec)
	return env
}

func objectSchema(properties, required string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","properties":%s,"required":%s,"additionalProperties":false}`,
		properties, required))
}

// writeMarker writes a small file into the workspace root, when one was
// granted, so a test can observe what the module did.
func writeMarker(req modproto.Request, name, body string) {
	if root, ok := req.Roots["workspace"]; ok {
		_ = os.WriteFile(filepath.Join(root.Path, name), []byte(body), 0o644)
	}
}

// startGrandchild starts a copy of this program that sleeps for seconds while
// holding the inherited stdout and stderr, and records its process ID in
// grandchild.pid in the workspace root.
func startGrandchild(req modproto.Request, seconds int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(self, "grandchild", strconv.Itoa(seconds))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	writeMarker(req, "grandchild.pid", strconv.Itoa(cmd.Process.Pid))
	return nil
}

func descriptor() *modproto.Descriptor {
	overlayPath := "agents/fake.md"
	skillPath := "skills/echo-usage/SKILL.md"

	return &modproto.Descriptor{
		Module:           moduleID,
		Name:             "Fake Module",
		Version:          "1.0.0",
		ProtocolVersions: []string{modproto.ProtocolID},
		Capabilities: []modproto.Capability{
			{
				ID: "fake.env", Title: "Report environment",
				Summary:       "Report the environment variable names this process can see.",
				RequestSchema: "fake.env.request/v1", ResultSchema: "fake.env.result/v1",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.where", Title: "Report working directory",
				Summary: "Report the working directory this process runs in.",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.echo", Title: "Echo",
				Summary:       "Echo structured input back, deterministically.",
				RequestSchema: "fake.echo.request/v1", ResultSchema: "fake.echo.result/v1",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
				Skills:  []string{"fake.echo-usage"},
			},
			{
				ID: "fake.report", Title: "Report",
				Summary:       "Write a deterministic report artifact into the workspace root.",
				RequestSchema: "fake.report.request/v1", ResultSchema: "fake.report.result/v1",
				ArtifactSchemas: []string{"fake.report/v1"},
				Effects:         modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.estimate.unpriced", Title: "Unpriced estimate",
				Summary:       "Estimate a call to an unpriced provider; cost stays unknown.",
				RequestSchema: "fake.echo.request/v1", ResultSchema: "fake.echo.result/v1",
				// cost_known=false is the declaration that makes a reported 0 a
				// violation the host can detect.
				Effects: modproto.Effects{Network: true, Provider: "unpriced-provider", CostKnown: false},
			},
			{
				ID: "fake.misbehave.stdout-pollution", Title: "Misbehave: stdout pollution",
				Summary: "Print progress to stdout before the envelope. Host must reject.",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.oversized", Title: "Misbehave: oversized output",
				Summary: "Inline a large payload instead of an artifact pointer. Host must bound it.",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.hang", Title: "Misbehave: hang",
				Summary: "Never return. Host must kill the process tree on deadline.",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.ignore-termination", Title: "Misbehave: ignore termination",
				Summary: "Ignore the request to stop. Host must kill it after the grace period.",
				Effects: modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.note-termination", Title: "Misbehave: note termination",
				Summary: "Wait to be asked to stop, record that it was, and stop.",
				Effects: modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.grandchild", Title: "Misbehave: grandchild",
				Summary: "Start a helper that holds stdout, then hang. Host must kill the whole tree.",
				Effects: modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.orphan-stdout", Title: "Misbehave: orphan holding stdout",
				Summary: "Start a helper that holds stdout, then answer and exit. Host must not wait for the helper.",
				Effects: modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.flood", Title: "Misbehave: flood stdout",
				Summary: "Write to stdout without end. Host must stop it at the output bound.",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.escape-root", Title: "Misbehave: escape root",
				Summary:         "Return an artifact path that escapes its declared root. Host must reject.",
				ArtifactSchemas: []string{"fake.report/v1"},
				Effects:         modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.wrong-size", Title: "Misbehave: wrong artifact size",
				Summary:         "Write a real file, then report a byte count that does not match it. Host must reject.",
				ArtifactSchemas: []string{"fake.report/v1"},
				Effects:         modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.wrong-digest", Title: "Misbehave: wrong artifact digest",
				Summary:         "Write a real file with the right size and a false digest. Host must reject.",
				ArtifactSchemas: []string{"fake.report/v1"},
				Effects:         modproto.Effects{Local: true, ExternalWrites: true, Provider: "local", CostKnown: true},
			},
			{
				ID: "fake.misbehave.undeclared-effect", Title: "Misbehave: undeclared effect",
				Summary: "Declare local and free, then report network use and unknown cost. Host must reject.",
				Effects: modproto.Effects{Local: true, Provider: "local", CostKnown: true},
			},
		},
		RequestSchemas: map[string]json.RawMessage{
			"fake.env.request/v1":    objectSchema(`{}`, `[]`),
			"fake.echo.request/v1":   objectSchema(`{"name":{"type":"string"},"repeat":{"type":"integer"}}`, `["name"]`),
			"fake.report.request/v1": objectSchema(`{"title":{"type":"string"}}`, `["title"]`),
		},
		ResultSchemas: map[string]json.RawMessage{
			"fake.env.result/v1":    objectSchema(`{"names":{"type":"array"}}`, `["names"]`),
			"fake.echo.result/v1":   objectSchema(`{"echoed":{"type":"string"},"count":{"type":"integer"}}`, `["echoed"]`),
			"fake.report.result/v1": objectSchema(`{"report":{"type":"string"}}`, `["report"]`),
		},
		ArtifactSchemas: map[string]json.RawMessage{
			"fake.report/v1": objectSchema(`{"title":{"type":"string"},"rows":{"type":"array"}}`, `["title"]`),
		},
		AgentOverlays: []modproto.Overlay{{
			ID: "fake.overlay", Title: "Fake module guidance",
			Path:   overlayPath,
			Digest: modproto.DigestSHA256([]byte(overlayContent)),
			Tokens: 60,
		}},
		Skills: []modproto.Skill{{
			ID: "fake.echo-usage", Title: "Using fake.echo",
			Summary: "When and how to call fake.echo.",
			Path:    skillPath,
			Digest:  modproto.DigestSHA256([]byte(skillContent)),
			Tokens:  40,
		}},
		Permissions: modproto.Permissions{
			FilesystemWrite: []string{"workspace"},
		},
		Requirements: []modproto.Requirement{},
	}
}

// The overlay and skill bodies are compiled in so the module ships a single
// self-contained binary for tests, while their digests are computed from these
// exact bytes -- so a host that verifies provenance gets a real match.
const overlayContent = "# Fake module\n\nUse fake.echo for deterministic checks. Never treat fake output as real work.\n"

const skillContent = "# Using fake.echo\n\nCall fake.echo with a `name`. It echoes deterministically and costs nothing.\n"
