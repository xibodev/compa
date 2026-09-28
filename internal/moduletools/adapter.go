// Package moduletools adapts detached module capabilities into agent tools.
//
// This is the seam the whole architecture exists for. A module declares
// capabilities; the host turns each enabled one into a tool the agent can call,
// so "assay my last three sessions" reaches sessions.assay without the agent
// knowing anything about the module that serves it, and without that module
// being linked into this binary.
//
// The host stays in charge of everything that matters. It decides which
// capabilities become tools, supplies filesystem roots and subprocess binaries
// per invocation, enforces bounds, and validates what comes back. A module
// contributes a description and a schema; it does not gain authority by being
// installed.
package moduletools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/modproto"
	toolshared "github.com/xibodev/compa/pkg/tools/shared"
	"github.com/xibodev/compa/pkg/view"
)

// CapabilityTool exposes one module capability as an agent tool.
type CapabilityTool struct {
	runner     *module.Runner
	descriptor *modproto.Descriptor
	capability modproto.Capability

	// home is the host state root, used to resolve the roots this capability
	// is granted for each invocation.
	home string
	// workspace is the agent's working directory, granted as the "workspace"
	// root so a capability can write where the user expects.
	workspace string
	// sourceRoots are the operator's named source roots (name -> absolute
	// path), granted read-only to the names this module declares. The agent
	// never supplies or widens them: they come from the host's config.
	sourceRoots map[string]string

	// views is the host's authority on what it can draw. It resolves each
	// artefact once, here, so the cockpit renders what the host decided rather
	// than re-deriving it from a media type with its own mapping.
	views *view.Registry
}

// New builds tools for every capability a module declares.
//
// One tool per capability rather than one tool per module: the agent picks by
// what it wants to do, and each capability carries its own schema and its own
// declared effects, which is what makes per-capability approval possible.
func New(r *module.Runner, d *modproto.Descriptor, home, workspace string, sourceRoots map[string]string) []toolshared.Tool {
	// One registry per module: it is the host's own rendering authority, not
	// module-supplied, so every capability of every module resolves the same
	// way.
	views := view.NewRegistry()
	tools := make([]toolshared.Tool, 0, len(d.Capabilities))
	for _, c := range d.Capabilities {
		tools = append(tools, &CapabilityTool{
			runner: r, descriptor: d, capability: c,
			home: home, workspace: workspace, sourceRoots: sourceRoots, views: views,
		})
	}
	return tools
}

// Name is the tool name the model sees.
//
// Capability IDs are already namespaced by module ("creative.tools.run",
// "sessions.assay"), but dots are not universally safe in tool names across
// providers, so they become underscores. The module ID is prefixed so two
// modules offering a similarly-named capability cannot collide.
func (t *CapabilityTool) Name() string {
	return ToolName(t.descriptor.Module, t.capability.ID)
}

// ArtifactMarker prefixes the one-line JSON description of a produced
// artefact. The cockpit scans assistant output for it to render artefact
// cards; the model reads the same line as text, so both see identical facts.
const ArtifactMarker = "@artifact "

// ToolName is the agent-facing name for a capability.
//
// Exported so the cockpit can show the same name the agent uses: a person
// reading "archive__sessions_assay" in a chat transcript should be able to find
// that capability on the Modules page.
func ToolName(moduleID, capabilityID string) string {
	safe := func(s string) string {
		return strings.NewReplacer(".", "_", "-", "_", "/", "_").Replace(s)
	}
	return safe(moduleID) + "__" + safe(capabilityID)
}

// PlaceInput puts a caller-supplied JSON object into a request.
//
// A capability's RequestSchema describes Request.Input, but some modules read
// arguments beside Input at the request root, so an explicit "input" key is
// used verbatim and sibling keys pass through at the root. Host-owned fields
// are never overwritten, so a caller cannot widen roots, forge a request ID, or
// extend a deadline by naming one.
//
// humanApproved says whether a PERSON authorized this specific invocation --
// true only for a deliberate act in the cockpit, never for anything the model
// produced. It decides whether an approval claim in the arguments is carried or
// stripped.
//
// The distinction is a parameter rather than a convention because the two
// callers look identical at the call site and mean opposite things: an agent
// tool call is the model's word, and a click on "Approve and run" is the user's.
// A convention would be one refactor away from silently treating them the same,
// which is exactly the bug this exists to prevent.
func PlaceInput(req *modproto.Request, raw json.RawMessage, humanApproved bool) error {
	if len(raw) == 0 {
		req.Input = json.RawMessage(`{}`)
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return fmt.Errorf("input must be a JSON object: %w", err)
	}
	if humanApproved {
		return placeApprovedArgs(req, args)
	}
	return placeArgs(req, args)
}

// Description is what the agent reads when deciding whether to call this.
//
// It states the declared effects in plain language, because the model choosing
// a cheap local capability over an expensive networked one is the first line of
// cost control -- long before the approval prompt, which is the last.
func (t *CapabilityTool) Description() string {
	var b strings.Builder
	b.WriteString(t.capability.Summary)

	e := t.capability.Effects
	var notes []string
	if e.Network {
		notes = append(notes, "reaches the network")
	}
	if e.ExternalWrites {
		notes = append(notes, "writes files")
	}
	if !e.CostKnown {
		// Deliberately blunt, and explicit about who can approve.
		//
		// "requires human approval" alone left the model believing it could
		// obtain that approval by filling in a consent object -- asked
		// directly, it answered that it needed approved_by and
		// paid_generation_approved. It cannot: the host strips approval claims
		// arriving through the agent, so a consent the model constructs is
		// discarded however the user phrases their permission.
		//
		// Saying so HERE matters more than saying it in the failure message,
		// because this is what the model reads while deciding, and the failure
		// message only arrives after it has already promised the user a result.
		notes = append(notes,
			"COST UNKNOWN and may bill real money; needs an approval only the "+
				"user can give, on the Modules page -- you cannot approve this "+
				"yourself and must not construct a consent field")
	} else if e.Provider != "" && e.Provider != "local" {
		notes = append(notes, "provider "+e.Provider)
	}
	if len(notes) > 0 {
		b.WriteString(" (")
		b.WriteString(strings.Join(notes, "; "))
		b.WriteString(")")
	}

	b.WriteString(fmt.Sprintf(" [module %s v%s]", t.descriptor.Module, t.descriptor.Version))
	return b.String()
}

// Parameters is the capability's declared request schema.
//
// The module's own schema is handed to the model directly rather than being
// re-described by the host: the module owns its domain, and any translation
// here would be a second source of truth that could drift.
func (t *CapabilityTool) Parameters() map[string]any {
	if id := t.capability.RequestSchema; id != "" {
		if doc, ok := t.descriptor.RequestSchemas[id]; ok {
			var schema map[string]any
			if err := json.Unmarshal(doc, &schema); err == nil {
				// A module may describe the whole request or just its input;
				// either way the agent supplies fields, and the adapter places
				// them correctly below.
				return sanitizeSchema(schema)
			}
		}
	}
	// A capability with no declared schema still has to be callable.
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": true,
	}
}

// Execute invokes the capability as a bounded detached process.
func (t *CapabilityTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	req := &modproto.Request{
		Capability: t.capability.ID,
		Roots:      GrantRoots(t.descriptor, t.home, t.workspace, t.sourceRoots),
		DeadlineMS: module.DefaultInvokeDeadlineMS,
	}
	// A capability that may bill is refused HERE, before the module runs.
	//
	// The agent cannot approve it -- approval is a person clicking on the
	// Modules page against the declared effects, and an approval arriving
	// through the model is stripped by design. So the honest answer is to
	// refuse and say where the button is, not to run it and hope the module
	// asks.
	//
	// The cockpit's invoke path gained this gate first; the agent path did not
	// have it, which is the same divergence between these two callers that has
	// produced several bugs already. Verified by driving the real agent: it
	// produced a paid capability's artefact with nobody having approved it.
	// A standing approval is the operator having already decided about THIS
	// capability, explicitly, in the environment the host runs in. It is not
	// the model deciding -- an approval arriving through the agent is stripped
	// -- and it is not the host inferring anything.
	//
	// Without it the operator's actual goal became impossible: every route to
	// a render goes through a cost_known:false capability, so "create a video
	// from simple chatting" could only ever be answered with "go and click on
	// another page".
	if NeedsApproval(t.descriptor, t.capability,
		PreApproved(t.descriptor.Module, t.capability.ID)) {
		return toolshared.ErrorResult(t.failureGuidance("consent_required",
			"this capability declares an unknown cost, so it may bill real"+
				" money and the host refused it before running", ""))
	}
	if err := placeArgs(req, args); err != nil {
		return toolshared.ErrorResult(err.Error())
	}
	module.GrantBinaries(t.descriptor, req)
	// Authority for this call, intersected with what the module declared.
	// A grant is not consent: reaching a paid provider is authorized here,
	// but whether to spend on THIS call is still the module's ask.
	module.ApplyGrants(t.descriptor, req, module.GrantAll())

	res, err := t.runner.Invoke(ctx, t.descriptor, req)

	// A host-side refusal is reported to the model as a failure it can reason
	// about, not as a crash. The model may legitimately retry with different
	// arguments, so the reason has to reach it.
	if err != nil {
		if res != nil && res.Stderr != "" {
			return toolshared.ErrorResult(fmt.Sprintf("%v\n\ndiagnostics:\n%s", err, truncate(res.Stderr, 1000)))
		}
		return toolshared.ErrorResult(err.Error())
	}

	env := res.Envelope
	if !env.OK {
		return toolshared.ErrorResult(t.failureGuidance(env.Error.Code, env.Error.Message, res.Stderr))
	}

	return toolshared.NewToolResult(t.renderForModel(env))
}

// failureGuidance is what the agent reads when a capability fails.
//
// It tells the agent NOT to improvise, and that instruction is the point.
//
// Running journey B end to end showed why: a capability returned a validation
// error, the agent abandoned the module, fell back to generic shell tools, and
// produced a plausible-looking video that was completely blank -- three
// byte-identical frames. It then reported success with a real path and a real
// duration. None of the module's QA gates ran, its consent gate never fired,
// and no provenance was recorded, because the work never went through the
// module at all.
//
// A module exists precisely because it knows things the agent does not. Doing
// the job another way is not a fallback; it is producing something nobody
// checked while claiming the module's authority for it. So a failure here says
// what went wrong, and says plainly that the answer is to fix the call or
// report the failure -- never to route around it.
func (t *CapabilityTool) failureGuidance(code, message, stderr string) string {
	var b strings.Builder

	if code != "" {
		fmt.Fprintf(&b, "%s failed: %s: %s", t.capability.ID, code, message)
	} else {
		fmt.Fprintf(&b, "%s failed: %s", t.capability.ID, message)
	}

	if s := strings.TrimSpace(stderr); s != "" {
		// Diagnostics are advisory and bounded, but they are often the only
		// thing that says WHY -- and without them an agent cannot tell a bad
		// request from a broken module, which is exactly when it improvises.
		fmt.Fprintf(&b, "\n\nmodule diagnostics:\n%s", truncate(s, 1200))
	}

	b.WriteString("\n\nDo NOT attempt this task with shell commands or other" +
		" general-purpose tools. This module owns this capability, and work done" +
		" outside it skips the validation, cost approval and provenance that make" +
		" the result trustworthy -- it would look like success while being" +
		" unverified.")

	consent := code == "consent_required" || strings.Contains(strings.ToLower(message), "consent")
	if !consent {
		b.WriteString(t.sourceRootNote())
	}

	if code == modproto.ErrInvalidRequest || code == "invalid_request" {
		b.WriteString(" Re-read this tool's schema and retry with corrected" +
			" arguments.")
	} else if isBrokenRuntimeFailure(code, message, stderr) {
		// A dependency the module ships is missing or incomplete.
		//
		// Nothing the agent can do to the REQUEST fixes this, so the generic
		// "correct it and retry" advice below is actively wrong: it invites a
		// retry loop against a failure that is identical every time, and a
		// model that exhausts retries is a model looking for another way to do
		// the job.
		//
		// Observed: a module's bundled Node renderer had a partially extracted
		// package, and the failure surfaced as "Cannot find module
		// './dist/index'" -- which reads like a bug in the module's code rather
		// than a broken install, and tells the operator nothing they can act
		// on.
		b.WriteString(" This is a MISSING DEPENDENCY inside the module's own" +
			" installation, not a problem with the request -- retrying it will" +
			" fail identically. Tell the user that this module's runtime is" +
			" incomplete and name the dependency from the diagnostics above, so" +
			" they can repair or reinstall the module. Do not retry, and do not" +
			" substitute another tool.")
	} else if consent {
		// Approval cannot be given in chat, so do not let the agent ask for it
		// there.
		//
		// The host strips approval claims arriving through the model -- it was
		// observed minting them from a sentence. Without this branch the agent
		// reads the generic "correct the request and retry" advice, asks the
		// user to approve in chat, receives an approval it cannot legitimately
		// carry, and reports that the module "wants a stricter consent format".
		// Observed end to end: the user did exactly what they were asked and
		// still failed, with the blame landing on the module.
		//
		// The host knows where approval actually lives, so it says so.
		fmt.Fprintf(&b, " This capability needs a human approval, and that"+
			" approval CANNOT be given in chat -- an approval you construct"+
			" from a message is not one the host will carry, however the user"+
			" phrases it. Do not ask the user to approve here and do not retry"+
			" with a reworded consent."+
			" Tell them to open the Modules page, expand the capability %q under"+
			" module %q, paste the request arguments into its input box, and"+
			" press \"Approve and run\". Name the CAPABILITY, not the tool: the"+
			" page lists capabilities, so a user told to look for a tool name"+
			" will not find it. Give them the exact JSON to paste.",
			t.capability.ID, t.descriptor.Module)
	} else if isPathFailure(code, message) {
		// The host knows something the agent cannot see: which directories
		// were actually granted for this call. A module resolves relative
		// paths against its OWN directory, so a bare filename that is obvious
		// to a person names nothing the module can find -- and without the
		// root list the agent's only recourse is to guess, which is how it
		// ends up inventing paths or abandoning the tool.
		b.WriteString(t.rootHint())
	} else {
		b.WriteString(" Either correct the request and retry, or tell the user" +
			" plainly that this capability is unavailable and why.")
	}

	return b.String()
}

// renderForModel turns an envelope into what the agent reads.
//
// Cost and artifacts are included deliberately. The agent is composing a
// multi-step task -- mine sessions, then produce a video -- and it needs the
// artifact path from step one to pass into step two, and needs to know when
// something cost money.
func (t *CapabilityTool) renderForModel(env *modproto.Envelope) string {
	var b strings.Builder

	if len(env.Result) > 0 {
		b.Write(env.Result)
	} else {
		b.WriteString("{}")
	}

	for _, w := range env.Warnings {
		b.WriteString("\nwarning: " + w)
	}

	// Artefacts are emitted as one machine-readable line each.
	//
	// The agent needs them because it composes multi-step tasks and must pass
	// step one's output into step two.
	//
	// The COCKPIT needs them too, and it can only read what the ASSISTANT says.
	// Tool results are not forwarded to the browser -- the chat channel sends
	// thoughts, tool CALLS and assistant content, and a tool's result is never
	// among them -- so a marker that stops here reaches the model and nothing
	// else. Observed directly: a seed.create artifact arrived in the tool
	// result, the model described it in prose, and no card rendered.
	//
	// So the model is asked to repeat the line verbatim. That keeps ONE format
	// for both readers, which is the property worth protecting: a second
	// structured channel could silently disagree with what the model was told,
	// and a card showing something the agent never saw is worse than no card.
	// The cost is that a card depends on the model complying; the alternative
	// costs correctness, and this failure is visible rather than silent.
	if len(env.Execution.Artifacts) > 0 {
		b.WriteString("\n\nThe following @artifact line(s) MUST be copied into" +
			" your reply exactly as written, each on its own line. The interface" +
			" renders them as file cards for the user; without them the user" +
			" sees no artefact. Describe them in your own words as well.")
	}
	for _, a := range env.Execution.Artifacts {
		line, err := json.Marshal(map[string]any{
			"id": a.ID, "kind": a.Kind, "path": a.Path, "root": a.Root,
			"media_type": a.MediaType, "presentation": a.Presentation,
			// The HOST decides how this renders, and says so.
			//
			// "presentation" is the module's HINT. Leaving the cockpit to turn a
			// media type into a renderer meant two independent mappings -- Go
			// here, TypeScript there -- and they disagreed on seven types: a
			// .docx rendered as a bare download rather than a document, and a
			// mermaid source as raw text rather than a diagram.
			//
			// A second implementation of a decision is a second answer waiting
			// to be different. The registry is the host's authority on what it
			// can draw, so it resolves once and the answer travels.
			"primitive": t.primitiveFor(a),
			"bytes":     a.Bytes, "digest": a.Digest,
			"title": a.Title, "module": t.descriptor.Module,
		})
		if err != nil {
			continue
		}
		b.WriteString("\n" + ArtifactMarker + string(line))
	}

	// Unknown cost is stated in words rather than left as a null the model has
	// to interpret.
	if c := env.Execution.ActualCost; c == nil {
		b.WriteString("\ncost: UNKNOWN (the module could not determine what this call cost)")
	} else if *c > 0 {
		b.WriteString(fmt.Sprintf("\ncost: %.4f", *c))
	}

	return b.String()
}

// placeArgs puts the model's arguments into the request.
//
// A capability's RequestSchema describes Request.Input, but some modules read
// arguments beside Input at the request root. Rather than requiring the model
// to know which, the host sends the arguments both ways: nested under "input"
// if the model supplied that key, and passed through at the root otherwise.
//
// Host-owned fields are never overwritten, so a model cannot widen filesystem
// roots, forge a request ID, or extend a deadline by naming one as an argument.
func placeArgs(req *modproto.Request, args map[string]any) error {
	if len(args) == 0 {
		req.Input = json.RawMessage(`{}`)
		return nil
	}

	// A model may not approve spending on the user's behalf.
	//
	// Consent is an AUTHORIZATION, not an argument. Demonstrated: told "I
	// approve any cost", the agent sent
	// consent:{approved_by:"web-user", paid_generation_approved:true} -- a
	// field it minted from a sentence, naming a user who never saw an approval
	// prompt. The host passed it straight through, so the only thing standing
	// between a chat message and a provider charge was that the provider
	// happened to be unconfigured.
	//
	// The host does not yet have an approval channel, so it cannot supply a
	// TRUE consent. What it can do is refuse to carry a false one: the field is
	// stripped, and the module's own gate then asks for consent through
	// whatever channel it trusts. A capability that needs approval fails closed
	// rather than proceeding on the model's word.
	//
	// Stripped rather than rejected because refusing the call would teach the
	// agent to retry without it, which is the same request minus the audit
	// trail.
	stripSelfMintedConsent(args)

	return encodeArgs(req, args)
}

// placeApprovedArgs is placeArgs for an invocation a PERSON authorized.
//
// Approval claims are carried rather than stripped, because here they are true:
// the cockpit only reaches this after a deliberate click on a capability whose
// declared effects were shown first. Host-owned fields are still reserved, so
// approving a run does not let the caller widen roots or extend a deadline.
func placeApprovedArgs(req *modproto.Request, args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}

	// The host RECORDS the approval rather than expecting it in the payload.
	//
	// The click is the approval: the page showed the declared effects, the
	// button said "Approve and run", and a person pressed it. Requiring the
	// user to also hand-write a consent object asks them to author the one
	// thing they cannot legitimately author -- and they will not know to,
	// because nothing tells them.
	//
	// Observed: the agent handed the user paste-ready JSON for the page, the
	// user pasted exactly that, and the run still failed consent_required
	// because the JSON carried no consent field. The instruction was right and
	// the outcome was still failure.
	//
	// A consent the caller already supplied is left alone: a module may define
	// fields the host does not know about, and overwriting them would be the
	// host inventing detail it cannot vouch for.
	if _, present := args["consent"]; !present {
		args["consent"] = map[string]any{
			"approved_by":              "operator",
			"paid_generation_approved": true,
			"note":                     "approved in the Compa cockpit against the capability's declared effects",
		}
	}

	return encodeArgs(req, args)
}

func encodeArgs(req *modproto.Request, args map[string]any) error {
	blob, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("could not encode arguments: %w", err)
	}

	if nested, ok := args["input"]; ok {
		inner, err := json.Marshal(nested)
		if err != nil {
			return fmt.Errorf("could not encode input: %w", err)
		}
		req.Input = inner
	} else {
		req.Input = blob
	}

	reserved := map[string]bool{
		"protocol": true, "capability": true, "request_id": true,
		"input": true, "roots": true, "grants": true, "binaries": true,
		"deadline_ms": true, "max_output_bytes": true,
	}
	req.Extra = map[string]json.RawMessage{}
	for k, v := range args {
		if reserved[k] {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			continue
		}
		req.Extra[k] = raw
	}
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "… (truncated)"
}

// sanitizeSchema makes a module-declared schema safe to hand to a model
// provider.
//
// Module output is untrusted input, and that applies to schemas as much as to
// results. Providers validate function schemas strictly and reject the whole
// request when one is malformed -- so a single module shipping "required": null
// takes down every tool call in the turn, including other modules'. Repairing
// it here keeps one module's defect from becoming a host-wide outage.
//
// The repairs are conservative: shapes are normalized, nothing is invented, and
// a field whose meaning is unclear is left alone.
func sanitizeSchema(schema map[string]any) map[string]any {
	// A JSON null decodes to a nil any, which marshals back to null. Providers
	// require "required" to be an array when present, so a null becomes an
	// empty array rather than being dropped -- dropping it would silently widen
	// the contract by making previously required fields optional.
	if v, ok := schema["required"]; ok && v == nil {
		schema["required"] = []any{}
	}

	// An object the module left OPEN stays open.
	//
	// The host's tool validator defaults to rejecting properties a schema does
	// not name, which is a deliberate prompt-injection defence for host tools
	// -- an "__inject" argument smuggled into read_file must be refused. But a
	// module dispatching many tools declares its per-call payload as an open
	// {"type":"object"} precisely because each tool has its own shape, and
	// applying the closed default there rejects every legitimate field.
	//
	// That is not a harmless refusal. Told its arguments were invalid by a host
	// that invented the restriction, the agent abandoned the module and did the
	// job with shell commands, producing an unverified result that looked like
	// success. A validator stricter than the contract it enforces pushes work
	// outside the boundary that makes it safe.
	//
	// So an open object is marked open EXPLICITLY, and a module that wants a
	// closed shape still gets one by saying "additionalProperties": false.
	if _, stated := schema["additionalProperties"]; !stated {
		if props, ok := schema["properties"].(map[string]any); !ok || len(props) == 0 {
			schema["additionalProperties"] = true
		}
	}
	if schema["type"] == nil {
		schema["type"] = "object"
	}
	if props, ok := schema["properties"]; !ok || props == nil {
		schema["properties"] = map[string]any{}
	}

	// Recurse into nested object and array schemas, since the same defect can
	// appear at any depth.
	if props, ok := schema["properties"].(map[string]any); ok {
		for name, raw := range props {
			if sub, ok := raw.(map[string]any); ok {
				props[name] = sanitizeSchema(sub)
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		schema["items"] = sanitizeSchema(items)
	}
	return schema
}

// isPathFailure reports whether a module's failure is about locating a file.
//
// Error codes below the protocol's own set are module-invented ("input_not_found"
// is one real module's), so this matches on the codes seen in practice AND on
// the message, rather than assuming a vocabulary no module agreed to. A false
// positive costs one extra line of advice; a false negative costs the agent the
// only information that would have let it construct a working path.
func isPathFailure(code, message string) bool {
	switch code {
	case modproto.ErrPathOutsideRoot, "input_not_found", "path_not_found", "file_not_found":
		return true
	}
	m := strings.ToLower(message)
	return strings.Contains(m, "does not exist") ||
		strings.Contains(m, "no such file") ||
		strings.Contains(m, "path is required")
}

// rootHint tells the agent which directories this call actually granted.
//
// A module resolves relative paths against its own installed directory, not the
// user's workspace, because inheriting the host's working directory made
// bundled-asset lookups depend on where the host happened to be launched. That
// is the right trade, but it means a relative path the USER supplies names
// nothing the module can find. Until the input contract carries a root, the
// workable answer is an absolute path inside a granted root -- so the host says
// what those are instead of leaving the agent to guess.
func (t *CapabilityTool) rootHint() string {
	roots := GrantRoots(t.descriptor, t.home, t.workspace, t.sourceRoots)
	if len(roots) == 0 {
		return ""
	}

	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("\n\nThis call granted these directories, and NOTHING outside" +
		" them is readable. A relative path is resolved by the module against" +
		" its own directory, not against these, so pass an absolute path inside" +
		" one of them:")
	for _, name := range names {
		r := roots[name]
		fmt.Fprintf(&b, "\n  %s (%s): %s", name, r.Mode, r.Path)
	}
	return b.String()
}

// sourceRootNote tells the agent which declared source roots this call ran
// WITHOUT, and what the user has to configure to supply them.
//
// The host is the only party that knows. A module whose data root was never
// granted fails with whatever its own error says -- often just "nothing found"
// -- and an agent reading that cannot tell a wrong request from a missing
// grant, so it retries, or goes looking for the data itself. Neither helps: the
// fix is a line of configuration only the user can add.
func (t *CapabilityTool) sourceRootNote() string {
	var missing []string
	for _, name := range SourceRootNames(t.descriptor) {
		path, configured := t.sourceRoots[name]
		if !configured {
			missing = append(missing, name)
			continue
		}
		if _, err := ResolveSourceRoot(name, path); err != nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return ""
	}
	quoted := make([]string, len(missing))
	for i, name := range missing {
		quoted[i] = strconv.Quote(name)
	}
	return fmt.Sprintf(" This call ran WITHOUT the source root(s) %s that this"+
		" module declares, because Compa's configuration supplies no usable path"+
		" for them. If the failure is about data the module could not find, that"+
		" is the likely cause: tell the user to add the location to Compa's"+
		" config file, for example \"modules\": {\"source_roots\": {%s:"+
		" \"<absolute path>\"}}, and do not search for the data yourself.",
		strings.Join(quoted, ", "), quoted[0])
}

// primitiveFor is the host's decision about how one artefact renders.
//
// A tool built without a registry still resolves rather than panicking or
// emitting nothing: an artefact that cannot be classified is offered as a
// download, which is the same floor the registry itself uses. Losing a
// renderer is a degraded card; losing the artefact is a lost result.
func (t *CapabilityTool) primitiveFor(a modproto.Artifact) string {
	if t.views == nil {
		return string(view.Download)
	}
	return string(t.views.Resolve(a.MediaType, a.Presentation))
}

// isBrokenRuntimeFailure reports whether a failure is a missing dependency
// inside the module's own installation rather than a bad request.
//
// The distinction decides what the agent does next. A bad request can be
// corrected and retried; a module whose bundled runtime is incomplete fails the
// same way every time, and an agent that keeps retrying eventually looks for
// another way to do the job -- which is the improvisation this boundary exists
// to prevent.
//
// Matched on the signatures that actually appear rather than on a code, because
// a module reports this as whatever its subprocess said. A false positive costs
// one sentence of advice; a false negative costs a retry loop.
func isBrokenRuntimeFailure(code, message, stderr string) bool {
	haystack := strings.ToLower(message + " " + stderr)
	for _, sig := range []string{
		"cannot find module",                                   // node
		"modulenotfounderror",                                  // python
		"no module named",                                      // python
		"error while loading shared libraries",                 // linux dynamic linker
		"is not recognized as an internal or external command", // windows shell
		"command not found",
	} {
		if strings.Contains(haystack, sig) {
			return true
		}
	}
	return code == modproto.ErrMissingRequirement
}

// consentFieldNames are the argument names that assert a human approved a cost.
//
// Matched by name rather than by capability, because the host cannot know which
// module invented which spelling -- and a field the host does not recognise is
// exactly the one that would slip through.
var consentFieldNames = map[string]bool{
	"consent":                  true,
	"paid_generation_approved": true,
	"cost_approved":            true,
	"approved_by":              true,
	"human_approved":           true,
}

// stripSelfMintedConsent removes approval claims from model-supplied arguments,
// at the top level and one level inside "input".
//
// One level is deliberate: modules place consent beside input or within it, and
// walking arbitrarily deep would start rewriting a module's own payload rather
// than the authorization envelope around it.
func stripSelfMintedConsent(args map[string]any) {
	for name := range consentFieldNames {
		delete(args, name)
	}
	if inner, ok := args["input"].(map[string]any); ok {
		for name := range consentFieldNames {
			delete(inner, name)
		}
	}
}
