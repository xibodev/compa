// Module lifecycle commands.
//
// Compa discovers installed module binaries, describes them, and invokes
// their capabilities as bounded detached processes. Modules are never imported
// as Go packages: they are separate programs speaking a JSON-over-CLI protocol,
// so a module can be upgraded, removed, or written in another language without
// touching the host.
//
//	compa-kernel modules
//	compa-kernel module-invoke <module> <capability> [json] [--source-root name=path]...
//	compa-kernel handoff
package main

import (
	"errors"
	"sort"
	"sync"

	"github.com/spf13/cobra"

	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/internal/moduletools"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/modproto"
)

// Home is the host's state root for the CLI.
//
// IT DELEGATES. There used to be a second fallback implementation here, and it
// disagreed with the host's whenever COMPA_HOME was unset: this side
// resolved to an executable-anchored `.local`, the agent to `~/.compa`.
// So `modules-add` installed somewhere the browser agent never looked, the CLI
// listed the module happily, and THE BROWSER SAW NOTHING -- silently, because
// discovery finding no modules looks exactly like none being installed.
//
// Masked throughout development because every launch set the variable.
//
// The dev-checkout intent is preserved and is now the host's own explicitly
// named fallback rather than a private one: a developer build still keeps its
// state beside the binary instead of writing into a user profile by surprise.
// What is gone is the SECOND ANSWER to the same question.
//
// Which one applies is decided by IsDevBuild, so the CLI and the host agree by
// construction rather than by both being careful.
func Home() string {
	if config.IsDevBuild() {
		return absOrSelf(config.DevHome())
	}
	return absOrSelf(config.GetHome())
}

// absOrSelf makes a path absolute, returning it unchanged when it cannot.
func absOrSelf(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func modulesDir() string { return filepath.Join(Home(), "modules") }

// discover finds installed module binaries.
//
// Installation is just a file on disk: the host asks each binary to describe
// itself rather than reading a registry, so there is no metadata that can drift
// out of sync with the executable.
// discover finds installed modules using the SAME implementation the cockpit
// uses.
//
// It previously had its own copy that scanned only flat files, so a module
// installed into its own directory by `modules add` was invisible to the CLI
// while the cockpit listed it. Two implementations of "what is installed"
// disagreeing is the class of bug this project keeps finding; there is now one.
func discover() ([]*module.Runner, error) {
	var runners []*module.Runner
	for _, in := range moduletools.Discover(context.Background(), Home()) {
		runners = append(runners, in.Runner)
	}
	return runners, nil
}

func cmdModules() error {
	runners, err := discover()
	if err != nil {
		return err
	}
	if len(runners) == 0 {
		fmt.Printf("no modules installed in %s\n", modulesDir())
		return nil
	}

	// Read at most once, and only if a module declares a source root.
	configured := sync.OnceValues(configuredSourceRoots)
	for _, r := range runners {
		d, res, err := r.Describe(context.Background())
		if err != nil {
			// A broken module is reported, never fatal: one bad module must not
			// take down discovery of the others.
			fmt.Printf("%-24s UNAVAILABLE  %v\n", filepath.Base(r.Binary), err)
			continue
		}

		status := "enabled"
		if dir, ok := moduletools.ModuleDir(Home(), r.Binary); ok && moduletools.Disabled(dir) {
			status = "disabled"
		}
		fmt.Printf("\n%s  v%s  (%s)  [%s]\n", d.Module, d.Version, d.Name, status)
		for _, c := range d.Capabilities {
			fmt.Printf("  %-30s %s\n", c.ID, c.Summary)
			fmt.Printf("  %-30s %s\n", "", effectLine(c.Effects))
		}
		if n := len(d.AgentOverlays); n > 0 {
			fmt.Printf("  overlays: %d   skills: %d\n", n, len(d.Skills))
		}
		if len(d.Permissions.Subprocess) > 0 {
			resolved, missing := module.ResolveBinaries(d.Permissions.Subprocess)
			fmt.Printf("  binaries: %d/%d resolved", len(resolved), len(d.Permissions.Subprocess))
			if len(missing) > 0 {
				fmt.Printf("   MISSING: %s", strings.Join(missing, " "))
			}
			fmt.Println()
		}
		// Declared source roots the host will not grant, said where the
		// operator looks before invoking anything.
		if len(moduletools.SourceRootNames(d)) > 0 {
			if sources, err := configured(); err != nil {
				fmt.Printf("  host warning: %v\n", err)
			} else {
				for _, w := range moduletools.SourceRootWarnings(d, sources) {
					fmt.Printf("  host warning: %s\n", w)
				}
			}
		}
		for _, w := range res.Envelope.Warnings {
			fmt.Printf("  warning: %s\n", w)
		}
	}
	return nil
}

// effectLine renders declared effects the way the cockpit must: a cost that is
// unknown is shown as unknown, never as free.
func effectLine(e modproto.Effects) string {
	var parts []string
	if e.Network {
		parts = append(parts, "network")
	}
	if e.ExternalWrites {
		parts = append(parts, "writes")
	}
	if e.CostKnown {
		parts = append(parts, "cost known")
	} else {
		parts = append(parts, "COST UNKNOWN - approval required")
	}
	return strings.Join(parts, ", ")
}

func cmdInvoke(moduleID, capability, input string) (*module.Result, error) {
	return cmdInvokeWithSourceRoots(moduleID, capability, input, nil)
}

// cmdInvokeWithSourceRoots invokes one capability, granting the module the
// configured source roots with overrides -- this command line's --source-root
// values -- taking precedence for the names they give.
func cmdInvokeWithSourceRoots(
	moduleID,
	capability,
	input string,
	overrides map[string]string,
) (*module.Result, error) {
	// A root this command line asked for must be usable, or nothing runs: an
	// explicit instruction for this one invocation is refused loudly, before
	// discovery, rather than quietly dropped.
	if err := validateSourceRootOverrides(overrides); err != nil {
		return nil, err
	}
	runners, err := discover()
	if err != nil {
		return nil, err
	}

	for _, r := range runners {
		d, _, err := r.Describe(context.Background())
		if err != nil || d.Module != moduleID {
			continue
		}
		if dir, ok := moduletools.ModuleDir(Home(), r.Binary); ok && moduletools.Disabled(dir) {
			return nil, fmt.Errorf("module %q is disabled; enable it before invoking capabilities", moduleID)
		}

		// Bind identity now that the descriptor has named the module, so the
		// invocation is checked against the module it claims to be.
		r.ModuleID = d.Module

		workspace, err := filepath.Abs(filepath.Join(Home(), "workspace"))
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			return nil, err
		}

		sources, err := invocationSourceRoots(d, overrides, configuredSourceRoots)
		if err != nil {
			return nil, err
		}
		// A declared root that will not be granted is said out loud: the
		// module usually fails without it, and its own error rarely says why.
		for _, w := range moduletools.SourceRootWarnings(d, sources) {
			fmt.Fprintf(os.Stderr, "note: %s %s\n", d.Module, w)
		}
		for _, name := range undeclaredSourceRoots(d, overrides) {
			fmt.Fprintf(os.Stderr, "note: --source-root %s was given, but %s declares no source root"+
				" of that name, so it is not granted\n", name, d.Module)
		}

		req := &modproto.Request{
			Capability: capability,
			Roots:      grantRoots(d, workspace, sources),
			DeadlineMS: module.DefaultInvokeDeadlineMS,
		}
		// The CLI takes one JSON blob and normally sends it as Input, which is
		// what a capability's RequestSchema describes. Some modules also read
		// root-level fields of their own alongside it, so any key the caller
		// supplies that is NOT part of the host's fixed envelope is passed
		// through at the root as well.
		//
		// This is a v1 convenience for driving real modules by hand, not a
		// protocol rule: Input remains the contract, and the host's own fields
		// always win so a module cannot capture them.
		if err := passThroughRootFields(req, input); err != nil {
			return nil, err
		}
		// Per-invocation authority: only the binaries this module declared.
		//
		// Two steps, not one. GrantBinaries resolves NAMES to absolute paths
		// (modules run with no PATH to search); ApplyGrants records what the
		// host actually authorizes. A module reads the grant to decide whether
		// it MAY shell out and the path to know what to run, so supplying only
		// the path left it holding an executable it was not permitted to use.
		//
		// The CLI called only the first. The agent and the cockpit called both,
		// which is why this failed here and nowhere else.
		if missing := module.GrantBinaries(d, req); len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "note: unresolved binaries: %s\n", strings.Join(missing, " "))
		}
		module.ApplyGrants(d, req, module.GrantAll())

		res, err := r.Invoke(context.Background(), d, req)
		if res != nil && res.Stderr != "" {
			fmt.Fprintf(os.Stderr, "--- module diagnostics ---\n%s", res.Stderr)
		}
		if err != nil {
			return res, err
		}
		return res, render(res)
	}
	return nil, fmt.Errorf("module %q is not installed in %s", moduleID, modulesDir())
}

// parseSourceRootFlags turns repeated --source-root name=path values into a
// map. It only splits them; validateSourceRootOverrides judges what they name.
func parseSourceRootFlags(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(values))
	for _, value := range values {
		name, path, ok := strings.Cut(value, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --source-root %q: expected name=<absolute path>", value)
		}
		if _, twice := out[name]; twice {
			return nil, fmt.Errorf("--source-root %s is given more than once", name)
		}
		out[name] = path
	}
	return out, nil
}

// validateSourceRootOverrides holds a command-line root to exactly what a
// configured one must be -- see moduletools.ResolveSourceRoot -- and reports
// the first that is not, in name order so the answer does not depend on map
// iteration.
func validateSourceRootOverrides(overrides map[string]string) error {
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := moduletools.ResolveSourceRoot(name, overrides[name]); err != nil {
			return fmt.Errorf("invalid --source-root %s=%s: %w", name, overrides[name], err)
		}
	}
	return nil
}

// invocationSourceRoots is what one invocation may be granted: the operator's
// configured source roots, with the command line's overrides on top.
//
// The config is read only when the module declares a source root the command
// line did not supply. An invocation that names every root it needs is decided
// by its command line alone, so a scripted run against fixture stores cannot
// pick up the real ones configured on the machine.
func invocationSourceRoots(
	d *modproto.Descriptor,
	overrides map[string]string,
	configured func() (map[string]string, error),
) (map[string]string, error) {
	sources := map[string]string{}
	for _, name := range moduletools.SourceRootNames(d) {
		if _, given := overrides[name]; given {
			continue
		}
		fromConfig, err := configured()
		if err != nil {
			return nil, err
		}
		for k, v := range fromConfig {
			sources[k] = v
		}
		break
	}
	for k, v := range overrides {
		sources[k] = v
	}
	return sources, nil
}

// undeclaredSourceRoots names the overrides the module has no use for, so a
// mistyped name is pointed out instead of silently doing nothing.
func undeclaredSourceRoots(d *modproto.Descriptor, overrides map[string]string) []string {
	declared := map[string]bool{}
	for _, name := range moduletools.SourceRootNames(d) {
		declared[name] = true
	}
	var out []string
	for name := range overrides {
		if !declared[name] {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// configuredSourceRoots reads modules.source_roots from the config file every
// other command reads.
func configuredSourceRoots() (map[string]string, error) {
	path := internal.GetConfigPath()
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return nil, fmt.Errorf("could not read source roots from %s: %w", path, err)
	}
	return cfg.Modules.SourceRoots, nil
}

// passThroughRootFields places one JSON blob from the command line into the
// request.
//
// A capability's RequestSchema describes Request.Input, so an explicit "input"
// key is used as Input verbatim and any sibling keys are passed through at the
// request root. A blob with no "input" key is treated as Input in its entirety,
// which is the common case.
//
// The passthrough exists because real modules already read some arguments
// beside Input rather than inside it, and a v1 that cannot drive them is not a
// working v1. Host-owned fields are never overwritten, so a module cannot
// capture protocol, roots, grants, binaries, or the bounds by naming them.
func passThroughRootFields(req *modproto.Request, input string) error {
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal([]byte(input), &supplied); err != nil {
		return fmt.Errorf("input must be a JSON object: %w", err)
	}

	nested, hasNested := supplied["input"]
	if hasNested {
		req.Input = nested
	} else {
		// No explicit "input" key: the blob IS the input, which is the common
		// case. It is also still passed through at the root, because a module
		// may read some arguments there instead -- and duplicating a value the
		// caller typed once is harmless, while dropping it is not.
		req.Input = json.RawMessage(input)
	}

	reserved := map[string]bool{
		"protocol": true, "capability": true, "request_id": true,
		"input": true, "roots": true, "grants": true, "binaries": true,
		"deadline_ms": true, "max_output_bytes": true,
	}

	req.Extra = make(map[string]json.RawMessage, len(supplied))
	for k, v := range supplied {
		if reserved[k] {
			continue
		}
		req.Extra[k] = v
	}
	return nil
}

// grantRoots maps the logical root NAMES a module declared onto real absolute
// paths, for this invocation only.
//
// This is where "installing a module grants nothing" becomes concrete: a module
// declares the names it needs, and the host decides what -- if anything -- each
// one points at. A name the host cannot supply is simply not granted, and the
// module fails closed rather than reaching for it another way.
//
// Source roots are supplied READ-ONLY, and only the ones the operator named: in
// the config, or with --source-root for this invocation. That guarantee is the
// host's, enforced on every path the module returns, and does not depend on the
// module behaving.
//
// It delegates to moduletools.GrantRoots -- the SAME implementation the agent
// uses. It used to be a second copy, and the copy did not grant a module its
// own bundle root, so a module could not find the runtime it ships with. The
// same module rendered video fine through the agent and failed through the CLI
// with a "runtime not found" error, which reads as a broken install rather than
// a missing grant.
//
// Two implementations of "what is this module allowed to see" is the same
// duplicated-decision shape that has produced several bugs here. There is now
// one.
func grantRoots(d *modproto.Descriptor, workspace string, sources map[string]string) map[string]modproto.Root {
	return moduletools.GrantRoots(d, Home(), workspace, sources)
}

// render is the normalized event: one shape for every module, carrying the
// facts the host is responsible for surfacing honestly.
func render(res *module.Result) error {
	e := res.Envelope

	status := "OK"
	if !e.OK {
		status = "FAILED"
	}
	fmt.Printf("[%s] %s / %s  (%s)\n", status, e.Module, e.Operation, res.Duration.Round(1e6))

	if !e.OK {
		fmt.Printf("  error   %s: %s\n", e.Error.Code, e.Error.Message)
		if e.Error.Retryable {
			fmt.Println("  retryable: yes")
		}
	}

	x := e.Execution
	fmt.Printf("  effects local=%v network=%v writes=%v provider=%q\n",
		x.Local, x.Network, x.ExternalWrites, x.Provider)
	fmt.Printf("  cost    estimated=%s actual=%s\n", cost(x.EstimatedCost), cost(x.ActualCost))

	for _, w := range e.Warnings {
		fmt.Printf("  warning %s\n", w)
	}
	// Host-side findings are labelled separately from the module's own, so a
	// reader can tell who is complaining about what.
	for _, w := range res.Warnings {
		fmt.Printf("  HOST    %s\n", w)
	}

	// Artifact cards: pointers, never inline payloads.
	for _, a := range x.Artifacts {
		fmt.Printf("  artifact %s\n", a.ID)
		fmt.Printf("    kind   %s\n", a.Kind)
		fmt.Printf("    path   %s (root %s)\n", a.Path, a.Root)
		fmt.Printf("    bytes  %d\n", a.Bytes)
		fmt.Printf("    digest %s\n", a.Digest)
	}

	if e.OK && len(e.Result) > 0 {
		var pretty any
		if err := json.Unmarshal(e.Result, &pretty); err == nil {
			blob, _ := json.MarshalIndent(pretty, "  ", "  ")
			if len(blob) > 1200 {
				blob = append(blob[:1200], []byte("\n  ... (truncated for display)")...)
			}
			fmt.Printf("  result  %s\n", blob)
		}
	}
	return nil
}

// cost renders the distinction the consent model rests on: null is unknown and
// must never be displayed as free.
func cost(c *float64) string {
	if c == nil {
		return "UNKNOWN"
	}
	return fmt.Sprintf("%.4f", *c)
}

// NewModulesCommand lists installed modules and their capabilities.
func NewModulesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "modules",
		Short: "List installed modules and their capabilities",
		RunE: func(_ *cobra.Command, _ []string) error {
			return cmdModules()
		},
	}
}

// NewModuleInvokeCommand runs one capability of one installed module.
//
// --source-root is deliberately local to this command: it lends a location for
// ONE invocation, and must never become a standing setting a browser-facing
// surface could reach. Standing roots belong in the config, under
// modules.source_roots.
func NewModuleInvokeCommand() *cobra.Command {
	var sourceRoots []string
	cmd := &cobra.Command{
		Use:   "module-invoke <module> <capability> [json]",
		Short: "Invoke a module capability as a bounded detached process",
		Long: "Invoke a module capability as a bounded detached process.\n\n" +
			"A module reads only the roots it declares. Source roots -- names such\n" +
			"as a notes or session store that only you can locate -- come from\n" +
			"modules.source_roots in the config; --source-root name=path adds or\n" +
			"overrides one for this invocation. Every source root is granted\n" +
			"read-only and must be an existing absolute directory or file.",
		Example: "  compa-kernel module-invoke some.module notes.search '{\"query\":\"deploy\"}' \\\n" +
			"    --source-root notes_store=/home/me/notes \\\n" +
			"    --source-root sessions_db=/home/me/data/sessions.db",
		Args: cobra.RangeArgs(2, 3),
		RunE: func(_ *cobra.Command, args []string) error {
			input := "{}"
			if len(args) > 2 {
				input = args[2]
			}
			overrides, err := parseSourceRootFlags(sourceRoots)
			if err != nil {
				return err
			}
			_, err = cmdInvokeWithSourceRoots(args[0], args[1], input, overrides)
			return err
		},
	}
	cmd.Flags().StringArrayVar(&sourceRoots, "source-root", nil,
		"grant a read-only source root for this invocation, as name=<absolute path>;"+
			" repeatable, and overrides modules.source_roots in the config for that name")
	return cmd
}

// NewModuleAddCommand installs a module binary into this host.
//
// This is the command a module's own installer shells out to. The host
// validates, chooses the destination, and derives identity from the
// descriptor, so a module never writes into host state nor needs to know the
// host's layout.
func NewModuleAddCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "modules-add <path-to-module-binary>",
		Short: "Install a detached module from a local binary",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			id, err := module.Install(context.Background(), Home(), args[0])
			if id != "" {
				fmt.Printf("installed module %s\n", id)
			}
			var partial *module.PartialInstall
			if errors.As(err, &partial) {
				// The module works; only its documentation is missing. Say so
				// rather than failing, because the consequence is otherwise
				// invisible: the agent behaves as if it documented nothing.
				for _, w := range partial.Warnings {
					fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
				}
				return nil
			}
			return err
		},
	}
}

// NewModuleRemoveCommand uninstalls a module.
func NewModuleRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "modules-remove <module-id>",
		Short: "Remove an installed module (its state is left intact)",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := module.Remove(Home(), args[0]); err != nil {
				return err
			}
			fmt.Printf("removed module %s\n", args[0])
			return nil
		},
	}
}

func NewModuleEnableCommand() *cobra.Command {
	return newModuleEnabledCommand("modules-enable", true)
}

func NewModuleDisableCommand() *cobra.Command {
	return newModuleEnabledCommand("modules-disable", false)
}

func newModuleEnabledCommand(name string, enabled bool) *cobra.Command {
	verb := "Enable"
	if !enabled {
		verb = "Disable"
	}
	return &cobra.Command{
		Use:   name + " <module-id>",
		Short: verb + " an installed module without removing its files or state",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			home := Home()
			for _, in := range moduletools.Discover(context.Background(), home) {
				if in.Descriptor == nil || in.Descriptor.Module != args[0] {
					continue
				}
				dir, ok := moduletools.ModuleDir(home, in.Runner.Binary)
				if !ok {
					return fmt.Errorf("module %q has no private install directory and cannot be toggled safely", args[0])
				}
				if err := moduletools.SetDisabled(dir, !enabled); err != nil {
					return err
				}
				state := "enabled"
				if !enabled {
					state = "disabled"
				}
				fmt.Printf("%s module %s\n", state, args[0])
				return nil
			}
			return fmt.Errorf("module %q is not installed in %s", args[0], modulesDir())
		},
	}
}
