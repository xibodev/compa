package moduletools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/modproto"
	toolshared "github.com/xibodev/compa/pkg/tools/shared"
)

// Installed is one discovered module.
type Installed struct {
	Runner     *module.Runner
	Descriptor *modproto.Descriptor
	Warnings   []string
	// HostWarnings are the HOST's findings about this module, kept apart from
	// what the module said about itself.
	//
	// They were merged, which made a stale self-diagnostic from a module and a
	// digest mismatch found by the host look identical on the Modules page --
	// and one of those the operator can act on. The invoke path had already
	// drawn this line; discovery had not.
	HostWarnings []string
	// V2 is the module's behavioural-contract standing: the pin outcome, and
	// the host's no-weakening verdict when the pin passed.
	//
	// Always populated, including for v1 modules and refusals. A nil-means-v1
	// field would make three outcomes share one observable, and telling them
	// apart is the entire job of the pin.
	V2 V2
	// Disabled is set when the user has turned this module off. It is still
	// DISCOVERED -- the Modules page must show it, with its capabilities, so
	// there is something to turn back on -- but it contributes no tools.
	Disabled bool
	// Err is set when the module could not describe itself. A broken module is
	// reported rather than fatal: one bad install must not hide the others.
	Err error
}

// ModulesDir is where installed modules live under the host state root. Only
// the host constructs this path; a module never needs to know it.
func ModulesDir(home string) string { return module.ModulesDir(home) }

// Discover finds installed modules and asks each to describe itself.
//
// Installation is a file on disk plus a successful describe. There is no
// registry file to drift out of sync with the executable: the binary is the
// source of truth about what it can do.
func Discover(ctx context.Context, home string) []Installed {
	root := ModulesDir(home)

	var found []Installed
	// A module may be installed as <modules>/<id>/<binary> or as a bare binary
	// directly in <modules>/. Both are accepted so a hand-dropped binary works
	// as well as one placed by `modules add`.
	for _, path := range candidateBinaries(root) {
		r := &module.Runner{Binary: path}
		d, res, err := r.Describe(ctx)
		if err != nil {
			found = append(found, Installed{Runner: r, Err: err})
			continue
		}
		// Bind identity now that the descriptor has named the module, so every
		// later invocation is checked against the module it claims to be.
		r.ModuleID = d.Module

		in := Installed{Runner: r, Descriptor: d}
		if dir, ok := ModuleDir(home, path); ok {
			in.Disabled = Disabled(dir)
		}
		if res != nil && res.Envelope != nil {
			in.Warnings = res.Envelope.Warnings

			// THE CONTRACT GATE, run against the DESCRIPTOR bytes rather than
			// raw stdout.
			//
			// contract_version lives inside envelope.result, so evaluating raw
			// describe output returns v1 for EVERY module -- silently, because
			// "no contract_version" is a legitimate answer meaning "v1 module".
			// A sibling lane hit exactly that on their first probe and nearly
			// reported "no problem, we interoperate" from a v1 result that was
			// an artifact of their input.
			//
			// Note what this does NOT change: the wire format. A v2 module
			// still declares protocol_versions ["xibodev.module/v1"] and still
			// passes v1 descriptor validation above -- measured against the
			// sibling's real binary, which ships both fields. Wire format and
			// behavioural contract are different axes, which is the whole
			// premise of the pin, so nothing about the v1 read path moves.
			in.V2 = evaluateV2(res.Envelope.Result)
		}
		// The HOST's own findings about what is on disk, alongside whatever the
		// module said about itself.
		//
		// Install checks declared digests too, but only at install time. A
		// module whose content drifts afterwards -- edited in place, or
		// rebuilt against different files -- installs cleanly and then simply
		// stops contributing its overlay, with nothing anywhere saying why.
		// Discovery runs every time the Modules page is opened, so this is
		// where a person can still find out.
		in.HostWarnings = append(in.HostWarnings, staleContentWarnings(filepath.Dir(path), d)...)
		in.HostWarnings = append(in.HostWarnings, costClaimWarnings(d)...)
		in.HostWarnings = append(in.HostWarnings, bundleRootWarnings(home, d)...)
		// A refused contract, or a v2 module whose projection weakens its own
		// Operations, is reported HERE rather than silently ignored. A module
		// that fails conformance still describes itself fine and still lists
		// capabilities, so without this the only symptom is a gate that never
		// fires -- invisible until it matters.
		in.HostWarnings = append(in.HostWarnings, v2Warnings(in.V2)...)
		found = append(found, in)
	}

	sort.Slice(found, func(i, j int) bool {
		return found[i].Runner.Binary < found[j].Runner.Binary
	})
	return found
}

func candidateBinaries(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}

	var out []string
	add := func(p string) {
		if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(p), ".exe") {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			out = append(out, abs)
		}
	}

	for _, e := range entries {
		full := filepath.Join(root, e.Name())
		if !e.IsDir() {
			add(full)
			continue
		}
		inner, err := os.ReadDir(full)
		if err != nil {
			continue
		}
		for _, f := range inner {
			if !f.IsDir() {
				add(filepath.Join(full, f.Name()))
			}
		}
	}
	return out
}

// RegisterTools turns every capability of every installed module into an agent
// tool, and returns a one-line summary per module for the agent's context.
//
// This is where "modules contribute tools" becomes real. The agent gains the
// ability to call a capability without knowing what a module is; the host keeps
// every decision about authority.
// The discovered set is returned alongside the summaries so a caller can
// compose module knowledge later without describing every module a second
// time -- each describe is a subprocess, and a per-turn rediscovery would
// make selecting a module cost more than using it.
//
// The operator's source roots are read here, once per registration, because
// the agent's composition root hands the module host nothing but its state
// root. A config reload re-registers the tools, so a changed root is picked up
// the same way every other tool setting is.
func RegisterTools(ctx context.Context, home, workspace string, register func(toolshared.Tool)) ([]string, []Installed) {
	var summaries []string

	installed := Discover(ctx, home)
	sources := agentSourceRoots(home, installed)
	for _, in := range installed {
		if in.Err != nil {
			logger.WarnCF("modules", "installed module could not describe itself", map[string]any{
				"binary": filepath.Base(in.Runner.Binary),
				"error":  in.Err.Error(),
			})
			continue
		}

		// A disabled module contributes NOTHING to the agent: no tools, and no
		// summary line either. Leaving the summary would describe abilities the
		// agent cannot reach, which is worse than silence -- it would try.
		if in.Disabled {
			continue
		}

		for _, w := range SourceRootWarnings(in.Descriptor, sources) {
			logger.WarnCF("modules", "module source root not granted", map[string]any{
				"module":  in.Descriptor.Module,
				"warning": w,
			})
		}
		for _, tool := range New(in.Runner, in.Descriptor, home, workspace, sources) {
			register(tool)
		}

		summaries = append(summaries, summarize(in.Descriptor))
		logger.InfoCF("modules", "module capabilities registered as agent tools", map[string]any{
			"module":       in.Descriptor.Module,
			"version":      in.Descriptor.Version,
			"capabilities": len(in.Descriptor.Capabilities),
		})
	}
	if len(summaries) > 0 {
		register(&KnowledgeTool{installed: installed})
		summaries = append(summaries, "Module guidance: call module_knowledge for available skill IDs and provenance, then load the relevant module/skill before using unfamiliar capabilities. No module selection is needed.")
	}
	return summaries, installed
}

// agentSourceRoots is the operator's configured source roots for the agent's
// tools, read only when an enabled module actually declares one -- a host whose
// modules ask for no source roots never depends on the config for them.
//
// A config that cannot be read grants none, and says so: the modules still
// register, and each one that needed a root fails closed rather than reading
// something nobody pointed it at.
func agentSourceRoots(home string, installed []Installed) map[string]string {
	needed := false
	for _, in := range installed {
		if in.Err == nil && !in.Disabled && len(SourceRootNames(in.Descriptor)) > 0 {
			needed = true
			break
		}
	}
	if !needed {
		return nil
	}
	sources, err := hostSourceRoots(home)
	if err != nil {
		logger.WarnCF("modules", "configured source roots could not be read, so none are granted", map[string]any{
			"error": err.Error(),
		})
		return nil
	}
	return sources
}

// summarize is the concise capability summary folded into the agent's context.
//
// One line per capability, deliberately: the prompt budget for an enabled
// module should be proportional to what it offers, and eagerly loading every
// module's full documentation is the failure the layered composition design
// exists to prevent.
func summarize(d *modproto.Descriptor) string {
	var b strings.Builder
	b.WriteString(d.Name)
	b.WriteString(" (module ")
	b.WriteString(d.Module)
	b.WriteString("):")
	for _, c := range d.Capabilities {
		b.WriteString("\n  - ")
		b.WriteString(c.ID)
		b.WriteString(": ")
		b.WriteString(c.Summary)
		if !c.Effects.CostKnown {
			b.WriteString(" [cost unknown; needs approval]")
		}
	}
	return b.String()
}

// GrantRoots maps the logical root NAMES a module declared onto real absolute
// paths, for one invocation.
//
// This is where "installing a module grants nothing" is concrete: a module
// declares names, the host decides what each points at, and each module's own
// state is confined to its own directory so modules cannot see or corrupt each
// other's.
//
// sources are the operator's named source roots (name -> absolute path), from
// the config and, for a command-line invocation, from --source-root. Only the
// names the module declared are consulted, each is granted READ-ONLY, and one
// that does not resolve is left out rather than replaced -- see
// ResolveSourceRoot and SourceRootWarnings. A nil map grants none.
func GrantRoots(d *modproto.Descriptor, home, workspace string, sources map[string]string) map[string]modproto.Root {
	return grantRoots(d, home, workspace, moduleBundleDir(home, d), sources)
}

func grantRoots(d *modproto.Descriptor, home, workspace, bundleDir string, sources map[string]string) map[string]modproto.Root {
	roots := map[string]modproto.Root{}
	if workspace != "" {
		roots["workspace"] = modproto.Root{Path: workspace, Mode: "rw"}
	}

	// A module's OWN installed directory, read-only.
	//
	// Modules run with no inherited environment and with their working
	// directory set to their install root, so a module that ships runtime
	// content -- a renderer project, a template pack -- has no way to name
	// where that content ended up. The host copied it there at install time and
	// is the only party that knows the path, so the host supplies it.
	//
	// Declared read-only, and that mode is a STATEMENT rather than a
	// filesystem-enforced boundary.
	//
	// Be precise about what it does and does not buy. A module runs with its
	// working directory set to this same install root, so the OS lets it write
	// here whatever mode the host declares -- observed directly: a tool given a
	// relative output path wrote its file into the module's own directory. The
	// mode tells an honest module what the host intends; it does not stop a
	// dishonest or buggy one.
	//
	// What actually catches content drifting after install is the digest check:
	// every declared overlay and skill is verified before it enters agent
	// context and REFUSED when it does not match, and staleContentWarnings
	// surfaces the same finding on the Modules page. So a module that edited
	// what was verified loses that content rather than smuggling it in.
	//
	// Granted only when the module actually declares it, like every other root:
	// a name the module never asked for is never supplied.
	if bundleDir != "" {
		for _, name := range d.Permissions.FilesystemRead {
			if !isBundleRootName(name) {
				continue
			}
			if abs, err := filepath.Abs(bundleDir); err == nil {
				roots[name] = modproto.Root{Path: abs, Mode: "ro"}
			}
		}
	}

	// Source roots, read-only: named by the module, located by the operator.
	//
	// The host holds no list of known stores and no default paths. A name is
	// granted only when the operator configured it, and only in the shape
	// ResolveSourceRoot accepts; anything else is left out -- never replaced
	// with a guess -- and SourceRootWarnings reports why.
	for _, name := range SourceRootNames(d) {
		path, configured := sources[name]
		if !configured {
			continue
		}
		clean, err := ResolveSourceRoot(name, path)
		if err != nil {
			continue
		}
		roots[name] = modproto.Root{Path: clean, Mode: "ro"}
	}

	for _, name := range d.Permissions.FilesystemWrite {
		if name == "workspace" {
			continue
		}
		dir, err := filepath.Abs(filepath.Join(home, "state", d.Module, name))
		if err != nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		roots[name] = modproto.Root{Path: dir, Mode: "rw"}
	}
	return roots
}

// isBundleRootName reports whether a declared read root refers to the module's
// own installed directory.
//
// The convention is a "_bundle" suffix, so a module names it in its own terms
// ("app_bundle") rather than the host imposing a single reserved word. The
// match is on the SUFFIX only: the host still supplies exactly one path, so two
// differently-named bundle roots resolve to the same directory rather than
// letting a module invent a second one.
func isBundleRootName(name string) bool {
	return name == "bundle" || strings.HasSuffix(name, "_bundle")
}

// moduleBundleDir is the directory a module was installed into.
//
// Install writes one directory per module id, and renames the binary to that id
// inside it. Deriving the DIRECTORY rather than the binary path is still the
// right call -- the directory is what a bundle root grants, and it stays correct
// if the naming rule for the binary ever changes.
//
// Returns "" when the module is not installed, so a module running from
// somewhere else is simply not granted a bundle root rather than being handed a
// path that does not exist.
func moduleBundleDir(home string, d *modproto.Descriptor) string {
	if d == nil || d.Module == "" {
		return ""
	}
	dir := filepath.Join(ModulesDir(home), d.Module)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return ""
	}
	return dir
}

// staleContentWarnings reports declared content whose bytes no longer match its
// declared digest.
//
// Such content is REFUSED when loaded into agent context, which is correct -- a
// module's documentation is untrusted input like anything else it produces. But
// that refusal is invisible: the module works, its capabilities work, and only
// its guidance quietly never appears. A refusal nobody can see is
// indistinguishable from no refusal at all.
//
// Unreadable content is reported for the same reason. Content that matches
// produces nothing, because a warning on every open trains people to ignore
// warnings.
func staleContentWarnings(dir string, d *modproto.Descriptor) []string {
	if d == nil || dir == "" {
		return nil
	}

	var out []string
	check := func(kind, id, rel, declared string) {
		if rel == "" || declared == "" {
			return
		}
		blob, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			out = append(out, fmt.Sprintf(
				"%s %q at %s is declared but not readable, so it cannot be loaded", kind, id, rel))
			return
		}
		if actual := modproto.DigestSHA256(blob); actual != declared {
			out = append(out, fmt.Sprintf(
				"%s %q at %s does not match its declared digest and is REFUSED when"+
					" loaded into agent context; the module likely changed this file"+
					" without rebuilding", kind, id, rel))
		}
	}

	for _, o := range d.AgentOverlays {
		check("overlay", o.ID, o.Path, o.Digest)
	}
	for _, sk := range d.Skills {
		check("skill", sk.ID, sk.Path, sk.Digest)
	}
	return out
}

// bundleRootWarnings reports a declared bundle root the host cannot supply.
//
// WHY THIS IS NOT COVERED BY staleContentWarnings. That function checks
// AgentOverlays and Skills -- content a module declares with a path and a
// digest. A bundle root is different: the module declares only a NAME
// ("app_bundle") and the host resolves it to the install directory. Nothing
// verified the directory was actually there.
//
// The failure is silent by construction. moduleBundleDir returns "" for a
// missing directory, so the root is simply NOT GRANTED -- which is the right
// behaviour, since handing over a path that does not exist is worse. But the
// module then describes fine, lists its capabilities fine, and fails at INVOKE
// time for a reason invisible from discovery.
//
// FOUND BY AUDITING A SIBLING LANE'S FINDING AGAINST THIS HOST. Their build
// script copied a renderer's package.json under `2>/dev/null || true`; when it
// failed, the module still described itself correctly and their own
// bundle-current check still passed, because neither looks at runtime content.
// A MODULE THAT DESCRIBES ITSELF IS NOT NECESSARILY A MODULE THAT CAN RUN.
// This is the host-side half of that: their real descriptor declares a bundle
// root and their renderer lives inside it.
//
// A warning rather than a refusal: the module may not need the root for every
// capability, and refusing a module outright for a root it might not use would
// be worse than an unusable one being visible.
func bundleRootWarnings(home string, d *modproto.Descriptor) []string {
	if d == nil {
		return nil
	}
	var declared []string
	for _, name := range d.Permissions.FilesystemRead {
		if isBundleRootName(name) {
			declared = append(declared, name)
		}
	}
	if len(declared) == 0 {
		return nil
	}

	dir := moduleBundleDir(home, d)
	if dir == "" {
		return []string{fmt.Sprintf(
			"declares bundle root(s) %v, but its install directory could not be"+
				" resolved, so the root is NOT SUPPLIED at invocation. The module"+
				" will describe itself and list capabilities normally and fail"+
				" only when it reaches for content it declared it needs",
			declared)}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{fmt.Sprintf(
			"declares bundle root(s) %v resolving to %s, which cannot be read: %v",
			declared, dir, err)}
	}
	// An EMPTY bundle directory is reported too. It resolves, so the root IS
	// granted -- and a granted root containing nothing is indistinguishable at
	// invoke time from one whose content was never installed.
	if len(entries) == 0 {
		return []string{fmt.Sprintf(
			"declares bundle root(s) %v resolving to %s, which is EMPTY. The root"+
				" is granted and contains nothing, so a capability depending on"+
				" bundled runtime content will fail with no indication that the"+
				" content was never installed", declared, dir)}
	}
	return nil
}

// costClaimWarnings reports a capability the host will gate DESPITE its own
// summary saying it never bills.
//
// This is NOT a contradiction in the module, and the warning used to say it
// was. cost_known declares whether a numeric AMOUNT is known -- not whether
// money may be spent. So a capability whose amount is genuinely unknowable and
// which genuinely never bills is declaring both facts correctly; v1 simply
// cannot express the second one.
//
// What it is instead: a place where the host's proxy gate visibly disagrees
// with the module's own prose, which is worth surfacing because the consequence
// is not cosmetic.
//
// Observed: a module's estimate capability, whose entire purpose is checking
// cost BEFORE committing to a paid run, declared cost_known:false while its
// summary said "never bills". Asked directly, the agent answered that the tool
// "may bill real money". Marking the cost-checking tool as possibly-billing
// discourages the one behaviour that makes a cost gate work.
//
// A warning rather than a refusal, and now phrased as a host limitation rather
// than an author error: the fix is a successor-contract signal for
// chargeability independent of amount, not a change to the module's
// declaration. Telling an author "one of these is wrong" when neither is sends
// them to correct something correct.
func costClaimWarnings(d *modproto.Descriptor) []string {
	if d == nil {
		return nil
	}

	// Deliberately narrow: only phrases that assert NO cost, so a summary
	// merely mentioning money does not trip it.
	freeClaims := []string{"never bills", "cost nothing", "costs nothing", "free and model-free"}

	var out []string
	for _, c := range d.Capabilities {
		if c.Effects.CostKnown {
			continue
		}
		summary := strings.ToLower(c.Summary)
		for _, claim := range freeClaims {
			if strings.Contains(summary, claim) {
				out = append(out, fmt.Sprintf(
					"capability %q declares cost_known:false, so this host gates it"+
						" for approval, but its own summary says %q. Both can be true:"+
						" cost_known reports whether the AMOUNT is known, not whether"+
						" money may be spent, and v1 has no way to declare"+
						" chargeability separately. The gate is this host being"+
						" pessimistic, not the module contradicting itself", c.ID, claim))
				break
			}
		}
	}
	return out
}
