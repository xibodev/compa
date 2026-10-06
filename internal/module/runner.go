// Package module executes detached module processes on behalf of the host.
//
// Every rule the protocol states is enforced here rather than assumed, because
// a module is a separate program -- possibly buggy, possibly hostile, and in
// this architecture deliberately written against an independent implementation
// of the contract.
//
// Three donor defects are fixed by construction, all observed in the upstream
// prototype this host replaces:
//
//   - stdout and stderr are captured SEPARATELY. The donor used CombinedOutput,
//     so any module diagnostic corrupted the JSON stream it was parsing.
//   - output is BOUNDED. The donor buffered unbounded child output into memory
//     and echoed malformed payloads back into model context.
//   - the whole PROCESS TREE is killed. The donor's context cancellation killed
//     only the direct child, orphaning grandchildren such as ffmpeg.
package module

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/xibodev/compa/v2/pkg/modproto"
	"github.com/xibodev/compa/v2/pkg/pathlink"
)

// Defaults applied when a caller does not specify bounds. They are deliberately
// modest: a module needing more must say so, so that "unbounded" is never the
// accidental default.
const (
	// DefaultDeadline is the Runner's own fallback for a caller that sets no
	// deadline at all. It is DELIBERATELY NOT the invocation default -- see
	// DefaultInvokeDeadlineMS, which is what every production caller passes and
	// what the contract documents.
	//
	// Two numbers, and the difference is not an oversight. This one predates
	// the measurement: it was chosen when a module invocation was a small
	// bounded thing, before a real Remotion render was observed at 1m55s
	// against a 120s budget. Every production path -- CLI, agent, cockpit --
	// sets DeadlineMS explicitly, so this fallback is reached only by a caller
	// inside this repo that constructed a Request by hand, which today means
	// tests.
	//
	// Kept at 60s rather than raised to match, because the two answer
	// different questions. A hand-built Request with no deadline is a caller
	// who has not thought about bounds; giving it the render budget would make
	// a forgotten field cost three minutes before anyone notices. The
	// invocation default is measured against real work; this is a guard
	// against absent-mindedness.
	//
	// If a production path ever reaches this, that is the bug -- not the
	// value. TestEveryProductionInvocationSetsItsOwnDeadline pins that.
	DefaultDeadline       = 60 * time.Second
	DefaultMaxOutputBytes = 1 << 20 // 1 MiB
	DefaultMaxStderrBytes = 64 << 10

	// describeDeadline is short because discovery must be cheap: the host may
	// run it during startup or a UI refresh, and a module that cannot describe
	// itself quickly is already misbehaving.
	describeDeadline = 10 * time.Second

	// DefaultInvokeDeadlineMS is how long a capability may run when the caller
	// does not say.
	//
	// One constant, because three callers each had their own: the CLI allowed
	// 120s while the agent and the cockpit allowed 180s, so a render that
	// worked through the browser failed through the CLI with
	// "command_timeout: node was cancelled or timed out" -- which reads as a
	// broken module rather than a shorter leash.
	//
	// Video rendering is the case that sets the floor. It is a wall-clock
	// budget rather than a promise: a module that needs longer should return a
	// job handle, not hold the host.
	//
	// The floor is measured, not chosen: the same render takes ~17s on an idle
	// machine and was observed at 2m32s while a frontend build, a test run and
	// the gateway competed for the same cores. A budget set from the idle case
	// would kill real work whenever the machine is busy, which is exactly when
	// a person is most likely to be waiting on it.
	DefaultInvokeDeadlineMS = 180000

	// killGrace is how long a module has to stop after it is asked to, before
	// its process tree is ended by force.
	killGrace = 3 * time.Second

	// pipeGrace bounds how long the host keeps reading output after the module
	// exited or was stopped. A grandchild holding stdout open used to stretch
	// a 1.5s deadline to the grandchild's own lifetime.
	pipeGrace = 2 * time.Second

	// MaxErrorText bounds module-supplied text -- an error message, a warning
	// -- before it reaches a tool result, the chat or a log. A module's error
	// message could otherwise be most of its 1 MiB envelope.
	MaxErrorText = 2 << 10
)

// Why a run was stopped early. They are context causes, so the error can say
// which deadline fired instead of blaming the module for the caller's.
var (
	errOwnDeadline    = errors.New("the module's own deadline expired")
	errOutputOverflow = errors.New("the module wrote more output than allowed")
)

// processStarts counts module processes started, so a test can tell a cached
// answer from a fresh run.
var processStarts atomic.Int64

// Runner executes one installed module binary.
type Runner struct {
	// Binary is the absolute path to the module executable.
	Binary string
	// ModuleID is the expected module ID; a descriptor claiming a different
	// one is rejected, so a binary cannot impersonate another module.
	ModuleID string

	// WorkDir is the working directory the module runs in, created when
	// needed. Empty means a fresh temporary directory per run. Installed
	// modules get ScratchDir: never the install directory, never the host's.
	WorkDir string

	// Digest is the binary's digest recorded at install time. When set, a
	// binary that no longer matches it is refused before it runs.
	Digest string

	MaxOutputBytes int
	MaxStderrBytes int
}

// Result is one completed module invocation, including the evidence needed to
// explain what happened when it went wrong.
type Result struct {
	Envelope *modproto.Envelope
	// Stderr is advisory diagnostics only: bounded, surfaced to humans, and
	// never parsed for control flow or protocol data.
	Stderr string
	// Truncated reports that output hit its bound. The envelope is then
	// unusable, since a truncated JSON document cannot be trusted even if it
	// happens to parse.
	Truncated bool
	Duration  time.Duration
	ExitCode  int
	// Warnings are host-side advisory findings about this invocation, distinct
	// from the module's own Envelope.Warnings.
	Warnings []string
}

// Describe runs `<binary> module describe --json` and returns a validated
// descriptor.
func (r *Runner) Describe(ctx context.Context) (*modproto.Descriptor, *Result, error) {
	res, err := r.exec(ctx, "describe", describeDeadline, r.maxOut(), "module", modproto.OperationDescribe, "--json")
	if err != nil {
		return nil, res, err
	}

	if err := modproto.ValidateEnvelope(res.Envelope, modproto.OperationDescribe, ""); err != nil {
		return nil, res, fmt.Errorf("%s: describe envelope is invalid: %w", modproto.ErrHostProtocolViolation, err)
	}
	if !res.Envelope.OK {
		return nil, res, fmt.Errorf("module reported describe failure: %s",
			BoundText(res.Envelope.Error.Message, MaxErrorText))
	}

	var d modproto.Descriptor
	if err := json.Unmarshal(res.Envelope.Result, &d); err != nil {
		return nil, res, fmt.Errorf("%s: descriptor is not decodable: %w", modproto.ErrHostInvalidJSON, err)
	}
	if err := modproto.ValidateDescriptor(&d); err != nil {
		return nil, res, fmt.Errorf("%s: descriptor is invalid: %w", modproto.ErrHostProtocolViolation, err)
	}

	// A binary installed as one module may not answer as another, or module
	// identity would be self-asserted and unenforceable.
	if r.ModuleID != "" && d.Module != r.ModuleID {
		return nil, res, fmt.Errorf("%s: binary installed as module %q describes itself as %q",
			modproto.ErrHostProtocolViolation, r.ModuleID, d.Module)
	}

	return &d, res, nil
}

// Invoke runs one capability and returns a fully validated envelope.
//
// The descriptor is required rather than optional: without it the host cannot
// check reported execution against declared effects, which is the check that
// keeps an unpriced call from rendering as free and a writing capability from
// routing around approval.
func (r *Runner) Invoke(ctx context.Context, d *modproto.Descriptor, req *modproto.Request) (*Result, error) {
	if d == nil {
		return nil, fmt.Errorf("%s: no descriptor to check this invocation against", modproto.ErrHostProtocolViolation)
	}
	// An undeclared capability is refused BEFORE the module starts. It used to
	// be checked only against the reply, after the module had already run
	// with whatever ID the caller chose -- and that ID is argv, so "--help"
	// or "--input=..." reached the module's flag parser.
	if err := checkInvocable(d, req.Capability); err != nil {
		return nil, err
	}
	if req.RequestID == "" {
		id, err := NewRequestID()
		if err != nil {
			return nil, err
		}
		req.RequestID = id
	}
	req.Protocol = modproto.ProtocolID
	req.Normalize()

	if req.MaxOutputBytes <= 0 {
		req.MaxOutputBytes = r.maxOut()
	}
	deadline := time.Duration(req.DeadlineMS) * time.Millisecond
	if deadline <= 0 {
		deadline = DefaultDeadline
		req.DeadlineMS = int(deadline / time.Millisecond)
	}

	// Roots must be absolute and canonical before they are handed over, so the
	// module never resolves a path and the host can check what comes back.
	for name, root := range req.Roots {
		abs, err := filepath.Abs(root.Path)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", name, err)
		}
		root.Path = filepath.Clean(abs)
		if root.Mode != "ro" && root.Mode != "rw" {
			return nil, fmt.Errorf("root %q has mode %q, want \"ro\" or \"rw\"", name, root.Mode)
		}
		req.Roots[name] = root
	}

	inputPath, cleanup, err := writeRequestFile(req)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	res, err := r.exec(ctx, "invocation", deadline, req.MaxOutputBytes,
		"module", modproto.OperationInvoke, req.Capability, "--input", inputPath)
	if err != nil {
		return res, err
	}

	if err := modproto.ValidateEnvelope(res.Envelope, modproto.OperationInvoke, req.RequestID); err != nil {
		return res, fmt.Errorf("%s: %w", modproto.ErrHostProtocolViolation, err)
	}
	if r.ModuleID != "" && res.Envelope.Module != r.ModuleID {
		return res, fmt.Errorf("%s: envelope reports module %q, want %q",
			modproto.ErrHostProtocolViolation, res.Envelope.Module, r.ModuleID)
	}

	// What it did, against what it said it would do. Effect over-reach is
	// surfaced as a warning rather than a refusal in v1; see
	// CheckExecutionAgainstDeclared for why.
	effectWarnings, err := modproto.CheckExecutionAgainstDeclared(d, req.Capability, &res.Envelope.Execution)
	for _, w := range effectWarnings {
		res.Warnings = append(res.Warnings, w.Error())
	}
	if err != nil {
		return res, fmt.Errorf("%s: %w", modproto.ErrHostProtocolViolation, err)
	}

	// Re-validate every returned path against the roots actually supplied. The
	// module already promised confinement; the host verifies it, because a
	// promise from an untrusted process is not a guarantee.
	//
	// Resolving the path is not enough on its own. A module that writes
	// somewhere else entirely and then REPORTS a well-formed in-root path
	// produces a record that passes every shape check and is simply false --
	// observed in practice: a tool wrote into the host's own repository root
	// while reporting the file as sitting in its granted root, byte count and
	// all. So the host also confirms something is actually there.
	//
	// This checks existence, not content. A digest check would be stronger, but
	// modules may legitimately report a digest they computed before a final
	// move, and the failure worth closing here is the artifact that cannot be
	// opened at all -- a card, a download link, and a provenance record for a
	// file that is not where the record says it is.
	for i, a := range res.Envelope.Execution.Artifacts {
		abs, err := ResolveArtifact(req, a)
		if err != nil {
			return res, fmt.Errorf("%s: artifact %d: %w", modproto.ErrPathOutsideRoot, i, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return res, fmt.Errorf("%s: artifact %d (%s): reported at %q in root %q, but nothing is there: %w",
				modproto.ErrHostProtocolViolation, i, a.ID, a.Path, a.Root, err)
		}

		// The reported size is a CLAIM, and the stat that proves the file
		// exists already carries the fact that settles it.
		//
		// It reaches the user: the artefact card prints it beside a download
		// link, so a module reporting 1 KB for a 4 GB file has the cockpit
		// telling someone a download is small when it is not. The same number
		// is what a person would check a transfer against.
		//
		// Zero is not treated as a claim -- a module may legitimately leave the
		// field unset, and inventing a mismatch there would warn on honest
		// modules, which is how a real warning gets ignored.
		if a.Bytes > 0 && info.Size() != a.Bytes {
			return res, fmt.Errorf("%s: artifact %d (%s): reported %d bytes but the file is %d",
				modproto.ErrHostProtocolViolation, i, a.ID, a.Bytes, info.Size())
		}

		// The digest is the strongest claim a module makes, and the one the
		// cockpit presents as PROVENANCE -- the artefact card prints it under a
		// comment saying it is what the host verified. It was not: the check
		// existed only on a staging path nothing in the product called, so
		// every card served through the cockpit showed an unchecked number.
		//
		// Reading the file to check it is the real cost here, unlike the size,
		// so it is bounded: past a limit the digest is left unverified rather
		// than making every render pay a full re-hash. A module that wants its
		// large artefact vouched for can report it in pieces.
		if err := verifyArtifactDigest(abs, a.Digest, info.Size()); err != nil {
			return res, fmt.Errorf("%s: artifact %d (%s): %w",
				modproto.ErrHostProtocolViolation, i, a.ID, err)
		}
	}

	return res, nil
}

// ResolveArtifact turns a module-reported artifact into an absolute host path,
// refusing anything that escapes its declared root.
//
// Both the root and the path are resolved through every kind of link,
// junctions included, so a link pointing outside the root cannot smuggle a
// path past a purely lexical check.
func ResolveArtifact(req *modproto.Request, a modproto.Artifact) (string, error) {
	root, ok := req.Roots[a.Root]
	if !ok {
		return "", fmt.Errorf("names root %q, which was not supplied for this invocation", a.Root)
	}
	if root.Mode != "rw" {
		return "", fmt.Errorf("claims to have written into root %q, which was supplied read-only", a.Root)
	}
	return resolveInRoot(root.Path, a.Path)
}

func resolveInRoot(rootPath, rel string) (string, error) {
	if modproto.IsAbsolutePath(rel) {
		return "", fmt.Errorf("path %q is absolute; it must be relative to its root", rel)
	}

	cleanRoot := filepath.Clean(rootPath)
	joined := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(rel)))

	// Lexical check first, so a non-existent path is still rejected.
	if !withinRoot(cleanRoot, joined) {
		return "", fmt.Errorf("path %q escapes root %q", rel, cleanRoot)
	}

	// Then the physical check, which catches links the lexical one cannot.
	//
	// The root goes through pathlink like the path does. It used
	// filepath.EvalSymlinks, which stopped following junctions in Go 1.23, so
	// a root that was a junction resolved to itself while every path inside
	// it resolved through the junction -- and every artifact was refused.
	realRoot, err := pathlink.Resolve(cleanRoot)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(realRoot); errors.Is(err, os.ErrNotExist) {
		// A root that does not exist yet is legitimate; the lexical check stands.
		return joined, nil
	}
	realPath, err := pathlink.Resolve(joined)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return joined, nil
		}
		return "", err
	}
	if !withinRoot(realRoot, realPath) {
		return "", fmt.Errorf("path %q resolves outside root %q via a link", rel, cleanRoot)
	}
	return joined, nil
}

// WithinRoot reports whether path is inside root, treating both as cleaned
// absolute-ish paths.
//
// Exported because three other places were doing this with strings.HasPrefix,
// which is wrong in a way that matters: "/srv/modules/acme-evil" has the prefix
// "/srv/modules/acme" and is a different directory. A module declaring
// "../acme-evil/overlay.md" escaped its own tree and the check accepted it.
//
// One implementation, so a fix here cannot leave a copy behind.
func WithinRoot(root, path string) bool { return withinRoot(root, path) }

func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	// ".." as the first segment means it climbed out.
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// exec runs the module and enforces every host-side bound.
//
// what names the run ("describe", "invocation") so a deadline error says which
// budget was spent.
func (r *Runner) exec(ctx context.Context, what string, deadline time.Duration, maxOut int, args ...string) (*Result, error) {
	// A binary that changed since it was installed is not run: the host
	// verified a different program.
	if err := verifyBinary(r.Binary, r.Digest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, notInstalledError(r.Binary)
		}
		return nil, err
	}

	dir, cleanupDir, err := r.workDir()
	if err != nil {
		return nil, fmt.Errorf("%s: could not prepare the module's working directory: %w",
			modproto.ErrHostSpawnFailed, err)
	}
	defer cleanupDir()

	callerCtx := ctx
	ctx, cancelDeadline := context.WithTimeoutCause(ctx, deadline, errOwnDeadline)
	defer cancelDeadline()
	ctx, stop := context.WithCancelCause(ctx)
	defer stop(nil)

	started := time.Now()

	cmd := exec.CommandContext(ctx, r.Binary, args...)
	// A module inherits no environment. Everything it may use arrives in the
	// request, so it cannot pick up host credentials or resolve paths from the
	// ambient environment.
	//
	// The one exception is a temp directory. Go's os.MkdirTemp resolves TMPDIR
	// (TEMP/TMP on Windows) and fails outright when none is set, so an empty
	// environment breaks any module that stages work in a temp dir -- which a
	// renderer must. That surfaced as "unable to create staging directory",
	// which reads like a permissions problem rather than a missing variable.
	//
	// This grants no authority a module did not already have: the host is
	// naming the same directory the OS would have offered, and everything that
	// confers real access -- roots, binaries, credentials -- still arrives only
	// in the request.
	cmd.Env = tempOnlyEnv()

	// A module runs in a scratch directory of its own, not beside its install.
	//
	// Running in the install directory put the module's verified files -- and
	// the host's state around them -- one relative path away, and a tool given
	// a relative output path wrote into its own install root. Content a module
	// ships is reached through its declared bundle root instead, an absolute
	// path the host supplies in the request.
	cmd.Dir = dir

	// The process tree is stopped by the tree, not by CommandContext's default
	// kill of the root alone: Cancel asks the whole tree to stop and ends it
	// after killGrace, and WaitDelay stops a grandchild that holds stdout from
	// holding the invocation with it.
	tree := newProcessTree()
	tree.prepare(cmd)
	cmd.Cancel = func() error {
		if errors.Is(context.Cause(ctx), errOutputOverflow) {
			tree.kill()
			return nil
		}
		return tree.interrupt()
	}
	cmd.WaitDelay = killGrace + pipeGrace

	var stdout, stderr bytes.Buffer
	// Separate pipes: merging them is what let donor diagnostics corrupt the
	// JSON stream. Output past the bound stops the module: what it writes
	// after that is discarded anyway, and a module flooding stdout would
	// otherwise run until its deadline.
	out := &limitedWriter{w: &stdout, limit: maxOut, onOverflow: func() { stop(errOutputOverflow) }}
	cmd.Stdout = out
	cmd.Stderr = &limitedWriter{w: &stderr, limit: r.maxErr()}

	if err := cmd.Start(); err != nil {
		// A module removed while the agent still holds its tools is the common
		// case here, and the OS error does not say so: Windows reports "the
		// directory name is invalid" for a missing install directory, which
		// reads like a broken module rather than an absent one.
		//
		// That distinction decides what an agent does next. "Broken" invites
		// working around it; "uninstalled" is a fact to report. Naming the
		// cause is the same reason failureGuidance exists.
		if _, statErr := os.Stat(r.Binary); errors.Is(statErr, os.ErrNotExist) {
			return nil, notInstalledError(r.Binary)
		}
		return nil, fmt.Errorf("%s: %w", modproto.ErrHostSpawnFailed, err)
	}
	processStarts.Add(1)
	if err := tree.started(cmd.Process); err != nil {
		_ = cmd.Wait()
		tree.finish(true)
		return nil, fmt.Errorf("%s: %w", modproto.ErrHostSpawnFailed, err)
	}

	waitErr := cmd.Wait()
	stoppedEarly := ctx.Err() != nil
	tree.finish(stoppedEarly)

	res := &Result{
		Stderr:   stderr.String(),
		Duration: time.Since(started),
		ExitCode: cmd.ProcessState.ExitCode(),
	}

	if out.truncated {
		res.Truncated = true
		return res, fmt.Errorf("%s: module wrote more than %d bytes to stdout, so it was stopped;"+
			" large results belong in an artifact pointer, not inline",
			modproto.ErrHostOutputTooLarge, maxOut)
	}

	if errors.Is(context.Cause(ctx), errOwnDeadline) {
		return res, fmt.Errorf("%s: module exceeded its %s %s deadline and its process tree was stopped",
			modproto.ErrHostTimeout, deadline, what)
	}

	// The CALLER's deadline is not the module's, and saying so matters: a
	// request whose HTTP or turn budget ran out first used to report that the
	// module exceeded its own deadline, which sends an author looking for a
	// slowness that is not there.
	if errors.Is(callerCtx.Err(), context.DeadlineExceeded) {
		return res, fmt.Errorf("%s: the caller's deadline expired after %s, before the module's own"+
			" %s %s deadline; the module's process tree was stopped",
			modproto.ErrHostTimeout, res.Duration.Round(time.Millisecond), deadline, what)
	}

	// A CANCELLED run is the host's doing, not the module's.
	//
	// Cancellation arrives when the gateway stops, a turn is abandoned, or the
	// caller goes away. The process tree is stopped correctly either way --
	// but without this the stopped process leaves truncated stdout, the
	// decoder reports "stdout is not a single JSON envelope", and a module
	// that behaved perfectly is recorded as having violated the protocol.
	//
	// The distinction matters beyond tidiness: host.protocol_violation is the
	// code that tells an operator a module is misbehaving, and spending it on
	// the host's own cancellation would teach them to ignore it.
	if callerCtx.Err() != nil {
		return res, fmt.Errorf("%s: the host cancelled this invocation and the module's process tree was stopped",
			modproto.ErrCancelled)
	}

	env, decodeErr := modproto.DecodeEnvelope(stdout.Bytes())
	if decodeErr != nil {
		// A module that produced no parseable envelope has failed the contract
		// regardless of its exit code. The exit code is reported as context,
		// never as the authority.
		return res, fmt.Errorf("%w (exit %d)", decodeErr, res.ExitCode)
	}
	res.Envelope = env

	// A non-zero exit with a well-formed envelope is a legitimate module
	// failure, so waitErr is not itself an error here: the envelope is the
	// authority and it already reports ok=false with a structured code. The
	// one exception is a helper that kept stdout open past pipeGrace, which
	// is reported rather than waited for.
	if errors.Is(waitErr, exec.ErrWaitDelay) {
		res.Warnings = append(res.Warnings, fmt.Sprintf(
			"a process the module started kept its output open for more than %s after it exited;"+
				" the host stopped waiting for it", pipeGrace))
	}

	return res, nil
}

// workDir returns the directory the module runs in and how to clean it up.
func (r *Runner) workDir() (string, func(), error) {
	if r.WorkDir != "" {
		if err := os.MkdirAll(r.WorkDir, 0o700); err != nil {
			return "", nil, err
		}
		return r.WorkDir, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "compa-module-*")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

func notInstalledError(binary string) error {
	return fmt.Errorf("%s: this module is no longer installed (%s is gone);"+
		" it cannot be used until it is installed again",
		modproto.ErrHostSpawnFailed, filepath.Base(binary))
}

// checkInvocable refuses a capability before the module is started: one the
// descriptor does not declare, and one that would read as a flag on the
// module's command line.
func checkInvocable(d *modproto.Descriptor, capability string) error {
	if strings.HasPrefix(capability, "-") {
		return fmt.Errorf("%s: capability %q starts with '-', so the module would read it as a flag;"+
			" it was not started", modproto.ErrUnknownCapability, BoundText(capability, 128))
	}
	for _, c := range d.Capabilities {
		if c.ID == capability {
			return nil
		}
	}
	return fmt.Errorf("%s: module %q declares no capability %q, so it was not started",
		modproto.ErrUnknownCapability, d.Module, BoundText(capability, 128))
}

// BoundText cuts s to at most max bytes, at a character boundary, and says how
// much was left out. Module-supplied text passes through it before it reaches
// a tool result, the chat or a log.
func BoundText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s… (%d more bytes not shown)", s[:cut], len(s)-cut)
}

func (r *Runner) maxOut() int {
	if r.MaxOutputBytes > 0 {
		return r.MaxOutputBytes
	}
	return DefaultMaxOutputBytes
}

func (r *Runner) maxErr() int {
	if r.MaxStderrBytes > 0 {
		return r.MaxStderrBytes
	}
	return DefaultMaxStderrBytes
}

// limitedWriter caps how much a child process can push into host memory.
//
// It keeps accepting writes after the limit and discards them, rather than
// returning an error: a write error would make the child fail in a confusing
// way, and the host has already decided the output is unusable. onOverflow,
// when set, is told once, so the host can stop the module.
type limitedWriter struct {
	w          io.Writer
	limit      int
	written    int
	truncated  bool
	onOverflow func()
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	// A write that adds nothing cannot overflow anything. This used to be an
	// unconditional `written >= limit` guard, which flagged an EMPTY flush at
	// the limit as truncation -- so a module whose output exactly filled the
	// buffer and then flushed had its complete result refused.
	if len(p) == 0 {
		return 0, nil
	}
	if l.written >= l.limit {
		l.overflow()
		return len(p), nil
	}
	remaining := l.limit - l.written
	if len(p) > remaining {
		l.overflow()
		if _, err := l.w.Write(p[:remaining]); err != nil {
			return 0, err
		}
		l.written = l.limit
		return len(p), nil
	}
	n, err := l.w.Write(p)
	l.written += n
	return n, err
}

func (l *limitedWriter) overflow() {
	if !l.truncated && l.onOverflow != nil {
		l.onOverflow()
	}
	l.truncated = true
}

// writeRequestFile spills the request to a temp file.
//
// A file rather than argv avoids command-line length limits and shell quoting
// entirely. It is created with 0600 because a request may carry prompts or
// other content the user would not want world-readable, and it is removed even
// when the invocation fails.
func writeRequestFile(req *modproto.Request) (string, func(), error) {
	blob, err := json.Marshal(req)
	if err != nil {
		return "", func() {}, err
	}

	// A debugging aid, bounded: a request can carry a whole prompt or document,
	// and the dump goes wherever stderr is logged.
	if os.Getenv("COMPA_DEBUG_REQUEST") != "" {
		fmt.Fprintf(os.Stderr, "[debug] request bytes: %s\n", BoundText(string(blob), MaxErrorText))
	}

	f, err := os.CreateTemp("", "compa-request-*.json")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { os.Remove(path) }

	if err := f.Chmod(0o600); err != nil && !errors.Is(err, os.ErrInvalid) {
		// Best-effort on platforms without POSIX modes.
		_ = err
	}
	if _, err := f.Write(blob); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

// tempOnlyEnv is the minimal environment a module process receives: a temp
// directory and nothing else.
//
// Everything that grants access travels in the request. This exists solely
// because the standard library's temp-file helpers read the environment and
// fail without it, and a module that cannot open a temp file cannot do most
// useful work.
func tempOnlyEnv() []string {
	dir := os.TempDir()
	if dir == "" {
		return []string{}
	}
	if runtime.GOOS == "windows" {
		return []string{"TEMP=" + dir, "TMP=" + dir}
	}
	return []string{"TMPDIR=" + dir}
}

// NewRequestID generates the host-side correlation ID a module must echo
// verbatim. It is host-generated so correlation never depends on trusting a
// module to be unique.
func NewRequestID() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return "req_" + hex.EncodeToString(buf[:]), nil
}

// maxDigestVerifyBytes bounds what the host will re-hash to check a reported
// digest.
//
// Chosen so an ordinary artefact -- a manifest, a report, a short clip -- is
// always verified, while a large render does not make every invocation pay a
// full read. Above the limit the digest is accepted unverified, which is what
// it was for every artefact before this check existed.
const maxDigestVerifyBytes = 64 << 20 // 64 MiB

// verifyArtifactDigest re-hashes an artefact and compares it to what the module
// reported.
//
// An empty digest is not a claim. A malformed one is: the protocol validator
// already requires "sha256:" plus 64 lowercase hex, so anything else reaching
// here means the envelope was not validated, and refusing is safer than
// silently skipping the check.
func verifyArtifactDigest(path, declared string, size int64) error {
	if declared == "" {
		return nil
	}
	if !modproto.ValidDigest(declared) {
		return fmt.Errorf("reported a malformed digest %q", declared)
	}
	if size > maxDigestVerifyBytes {
		return nil
	}

	blob, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("could not be read to verify its digest: %w", err)
	}
	if got := modproto.DigestSHA256(blob); got != declared {
		return fmt.Errorf("reported digest %s but the file hashes to %s", declared, got)
	}
	return nil
}
