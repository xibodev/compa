// Module lifecycle commands.
//
// Compa discovers installed module binaries, describes them, and invokes
// their capabilities as bounded detached processes. Modules are never imported
// as Go packages: they are separate programs speaking a JSON-over-CLI protocol,
// so a module can be upgraded, removed, or written in another language without
// touching the host.
//
//	compa-kernel modules
//	compa-kernel module-invoke <module> <capability> [json] [--source-root name=path]... [--approve]
package main

import (
	"errors"
	"io/fs"
	"sort"
	"sync"

	"github.com/spf13/cobra"

	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/internal/module"
	"github.com/xibodev/compa/v3/internal/moduletools"
	"github.com/xibodev/compa/v3/pkg/approval"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modproto"
)

// Home is the host's state root for the CLI: config.GetHome, the one the
// agent uses too, so a module the CLI installs is the one the agent finds.
func Home() string {
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

// discover finds installed modules using the SAME implementation the cockpit
// uses.
//
// It previously had its own copy that scanned only flat files, so a module
// installed into its own directory by `modules add` was invisible to the CLI
// while the cockpit listed it. Two implementations of "what is installed"
// disagreeing is the class of bug this project keeps finding; there is now one.
// A module discovery refused (a broken describe, a duplicate ID, a refused
// contract) comes back with Err set and no descriptor, and is never invoked.
func discover() []moduletools.Installed {
	return moduletools.Discover(context.Background(), Home())
}

func cmdModules() error {
	installed := discover()
	if len(installed) == 0 {
		fmt.Printf("no modules installed in %s\n", modulesDir())
		return nil
	}

	// Read at most once, and only if a module declares a source root.
	configured := sync.OnceValues(configuredSourceRoots)
	for _, in := range installed {
		if in.Err != nil || in.Descriptor == nil {
			// A broken module is reported, never fatal: one bad module must not
			// take down discovery of the others.
			fmt.Printf("%-24s UNAVAILABLE  %v\n", filepath.Base(in.Runner.Binary),
				module.BoundText(fmt.Sprint(in.Err), module.MaxErrorText))
			continue
		}
		d := in.Descriptor

		status := "enabled"
		if in.Disabled {
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
		for _, w := range in.Warnings {
			fmt.Printf("  warning: %s\n", w)
		}
		for _, w := range in.HostWarnings {
			fmt.Printf("  host warning: %s\n", w)
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
		parts = append(parts, "COST UNKNOWN")
	}
	return strings.Join(parts, ", ")
}

// invokeModule invokes one capability, granting the module the configured
// source roots with overrides -- this command line's --source-root values --
// taking precedence for the names they give. Without approve, the operator's
// --approve, a capability the approval policy asks about is refused.
func invokeModule(
	moduleID,
	capability,
	input string,
	overrides map[string]string,
	approve bool,
) (*module.Result, error) {
	// A root this command line asked for must be usable, or nothing runs: an
	// explicit instruction for this one invocation is refused loudly, before
	// discovery, rather than quietly dropped.
	if err := validateSourceRootOverrides(overrides); err != nil {
		return nil, err
	}
	if err := module.CheckID(moduleID); err != nil {
		return nil, err
	}

	for _, in := range discover() {
		if in.Err != nil || in.Descriptor == nil || in.Descriptor.Module != moduleID {
			continue
		}
		if in.Disabled {
			return nil, fmt.Errorf("module %q is disabled; enable it before invoking capabilities", moduleID)
		}
		// Discovery bound the runner to the module the descriptor names, so
		// the invocation is checked against the module it claims to be.
		r, d := in.Runner, in.Descriptor

		// Decided before anything runs, by the approval policy with origin
		// cli, as the agent's calls and the Modules page's runs are decided.
		// Without this an agent with the exec tool could run a capability the
		// policy asks about or denies by calling this command.
		policy, err := cliApprovalPolicy()
		if err != nil {
			return nil, err
		}
		approved, err := invokeApproval(policy, d, capability, approve)
		if err != nil {
			return nil, err
		}

		workspace, err := moduleWorkspace()
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
		// The CLI takes one JSON blob and places it the way the cockpit does:
		// an explicit "input" key is Input and its siblings pass through at the
		// root; otherwise the blob is Input and is also passed through. Host
		// fields always win. Approval claims in the blob are stripped unless
		// the operator approved this run, which the host then records.
		if err := moduletools.PlaceInput(req, json.RawMessage(input), approved); err != nil {
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
		module.ApplyGrants(d, req, module.GrantsFor(approved))

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

// moduleWorkspace is the workspace the config gives the agents, so a module
// invoked here reads and writes where the agent's would. Without a readable
// config it is the workspace under the state root.
func moduleWorkspace() (string, error) {
	workspace := ""
	if path := internal.GetConfigPath(); path != "" {
		if _, statErr := os.Stat(path); statErr == nil {
			if cfg, err := config.LoadConfig(path); err == nil {
				workspace = cfg.WorkspacePath()
			}
		}
	}
	if strings.TrimSpace(workspace) == "" {
		workspace = filepath.Join(Home(), "workspace")
	}
	return filepath.Abs(workspace)
}

// cliApprovalPolicy is the owner's approval policy (tools.approval) from the
// config every other command reads. Without a config it is the default policy;
// a config that cannot be read refuses the run rather than guessing.
func cliApprovalPolicy() (approval.Policy, error) {
	path := internal.GetConfigPath()
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return approval.DefaultPolicy(), nil
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		return approval.Policy{}, fmt.Errorf("could not read the approval policy from %s: %w", path, err)
	}
	return cfg.Tools.Approval, nil
}

// invokeApproval decides a run by policy, with origin cli, before the module
// starts. It returns whether the run is approved: a rule allows it, or the
// policy asks and the operator passed --approve. A run the default allows is
// not approved; one the policy asks about without --approve, denies or hides
// is refused, and so is a capability the module does not declare: its effects
// are unknown.
func invokeApproval(policy approval.Policy, d *modproto.Descriptor, capability string, approve bool) (bool, error) {
	c, ok := lookupCapability(d, capability)
	if !ok {
		return false, fmt.Errorf("module %q declares no capability %q, so it was not started", d.Module, capability)
	}
	decision := policy.Decide(moduletools.ApprovalTool(d, c), approval.OriginCLI)
	switch decision.Action {
	case approval.Allow:
		return decision.Approved(), nil
	case approval.Ask:
		if !approve {
			return false, fmt.Errorf("%s needs your approval before it runs. Check its declared effects with"+
				" `compa-kernel modules`, then run this command again with --approve", c.ID)
		}
		return true, nil
	default:
		return false, fmt.Errorf("%s is denied by the approval policy", c.ID)
	}
}

func lookupCapability(d *modproto.Descriptor, id string) (modproto.Capability, bool) {
	for _, c := range d.Capabilities {
		if c.ID == id {
			return c, true
		}
	}
	return modproto.Capability{}, false
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
	var approve bool
	cmd := &cobra.Command{
		Use:   "module-invoke <module> <capability> [json]",
		Short: "Invoke a module capability as a bounded detached process",
		Long: "Invoke a module capability as a bounded detached process.\n\n" +
			"A module reads only the roots it declares. Source roots -- names such\n" +
			"as a notes or session store that only you can locate -- come from\n" +
			"modules.source_roots in the config; --source-root name=path adds or\n" +
			"overrides one for this invocation. Every source root is granted\n" +
			"read-only and must be an existing absolute directory or file.\n\n" +
			"The approval policy (tools.approval) decides each run. A capability it\n" +
			"asks about -- by default one with an unknown cost, network reach or\n" +
			"external writes -- runs only with --approve; one it denies or hides\n" +
			"does not run.",
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
			_, err = invokeModule(args[0], args[1], input, overrides, approve)
			return err
		},
	}
	cmd.Flags().StringArrayVar(&sourceRoots, "source-root", nil,
		"grant a read-only source root for this invocation, as name=<absolute path>;"+
			" repeatable, and overrides modules.source_roots in the config for that name")
	cmd.Flags().BoolVar(&approve, "approve", false,
		"approve this run of a capability the approval policy asks about, against its declared effects."+
			" This is your decision as the operator: anything that can run this command, including"+
			" an agent with the exec tool, can pass it, so never put it in a command you let an agent run")
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
			// The ID is checked before anything turns it into a path:
			// `modules-remove ..` used to delete the whole Compa home.
			if err := module.CheckID(args[0]); err != nil {
				return err
			}
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
			if err := module.CheckID(args[0]); err != nil {
				return err
			}
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
