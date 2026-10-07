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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/xibodev/compa/v3/internal/module"
	"github.com/xibodev/compa/v3/pkg/approval"
	"github.com/xibodev/compa/v3/pkg/modproto"
	toolshared "github.com/xibodev/compa/v3/pkg/tools/shared"
	"github.com/xibodev/compa/v3/pkg/view"
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
//
// Providers accept tool names of at most 64 letters, digits, '_' and '-', and
// reject the WHOLE request when one tool's name is anything else -- so one
// module's odd capability ID used to take down every tool call. Every other
// character becomes '_' ('-' too, as it always did, so existing names are
// unchanged), and a name past the limit is cut and given a digest suffix so it
// stays distinct. Names that still collide are refused at registration.
func ToolName(moduleID, capabilityID string) string {
	safe := func(s string) string {
		return strings.Map(func(r rune) rune {
			if r == '_' || r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				return r
			}
			return '_'
		}, s)
	}
	name := safe(moduleID) + "__" + safe(capabilityID)
	if len(name) > maxToolName {
		sum := sha256.Sum256([]byte(moduleID + "\x00" + capabilityID))
		suffix := "_" + hex.EncodeToString(sum[:4])
		name = name[:maxToolName-len(suffix)] + suffix
	}
	return name
}

// maxToolName is the longest tool name providers accept.
const maxToolName = 64

// maxResultForModel bounds the module result text one tool call adds to the
// conversation.
const maxResultForModel = 128 << 10

// PlaceInput puts a caller-supplied JSON object into a request.
//
// A capability's RequestSchema describes Request.Input, but some modules read
// arguments beside Input at the request root, so an explicit "input" key is
// used verbatim and sibling keys pass through at the root. Host-owned fields
// are never overwritten, so a caller cannot widen roots, forge a request ID, or
// extend a deadline by naming one.
//
// approved says whether the operator approved this call under the approval
// policy: a rule that allows it, or an answered ask ("Approve and run",
// --approve, or the owner's /approve). An approved call carries the approval
// claims in the arguments and records the host's own; any other call has them
// stripped. The agent's tool strips what the model wrote either way.
func PlaceInput(req *modproto.Request, raw json.RawMessage, approved bool) error {
	if len(raw) == 0 {
		req.Input = json.RawMessage(`{}`)
		return nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return fmt.Errorf("input must be a JSON object: %w", err)
	}
	return placeInput(req, args, approved)
}

func placeInput(req *modproto.Request, args map[string]any, approved bool) error {
	if approved {
		return placeApprovedArgs(req, args)
	}
	return placeArgs(req, args)
}

// Description is what the agent reads when deciding whether to call this.
//
// It states the declared effects in plain language, because the model choosing
// a cheap local capability over an expensive networked one is the first line of
// cost control -- long before the approval policy, which is the last.
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
		notes = append(notes, "COST UNKNOWN and may bill real money")
	}
	if e.Provider != "" && e.Provider != "local" {
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
//
// The approval policy decided this call before it got here: Execute runs what
// the policy let through, and refuses nothing on its own.
func (t *CapabilityTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	// Disabling a module takes effect at once. The tools were registered when
	// the gateway started, and without this check they kept working until a
	// restart while the marker file said they were not offered.
	if t.runner != nil {
		if dir, ok := ModuleDir(t.home, t.runner.Binary); ok && Disabled(dir) {
			return toolshared.ErrorResult(fmt.Sprintf("%s is not available: module %q is DISABLED,"+
				" so its capabilities are not offered. Tell the user it can be re-enabled on the"+
				" Modules page; do not do this task another way.", t.capability.ID, t.descriptor.Module))
		}
	}
	req, err := t.request(ctx, args)
	if err != nil {
		return toolshared.ErrorResult(err.Error())
	}

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

// request builds the module request for one call with the model's arguments.
//
// The operator's approval travels in ctx (approval.Approved): a rule that
// allows the call, or the owner answering an ask. Only an approved call may
// publish, and only for one does the host record the approval for the module.
// Approval claims the model wrote are stripped either way: consent is an
// authorization, not an argument.
func (t *CapabilityTool) request(ctx context.Context, args map[string]any) (*modproto.Request, error) {
	approved := approval.Approved(ctx)
	req := &modproto.Request{
		Capability: t.capability.ID,
		Roots:      GrantRoots(t.descriptor, t.home, t.workspace, t.sourceRoots),
		DeadlineMS: module.DefaultInvokeDeadlineMS,
	}
	stripSelfMintedConsent(args)
	if err := placeInput(req, args, approved); err != nil {
		return nil, err
	}
	module.GrantBinaries(t.descriptor, req)
	// Authority for this call, intersected with what the module declared. A
	// grant is not consent: reaching a paid provider is authorized here, but
	// whether to spend on THIS call is still the module's ask.
	module.ApplyGrants(t.descriptor, req, module.GrantsFor(approved))
	return req, nil
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

	// A module's error message is its own text and could be most of its
	// output bound; the agent needs the gist, not a megabyte.
	code = module.BoundText(code, 128)
	message = module.BoundText(message, module.MaxErrorText)
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
		// The module asks for an approval this call did not carry: the
		// approval policy let it run without the owner's approval, so the host
		// recorded none.
		//
		// The host strips approval claims arriving through the model -- it was
		// observed minting them from a sentence. Without this branch the agent
		// reads the generic "correct the request and retry" advice, asks the
		// user to approve in chat, receives an approval it cannot legitimately
		// carry, and reports that the module "wants a stricter consent format".
		// Observed end to end: the user did exactly what they were asked and
		// still failed, with the blame landing on the module.
		//
		// The host knows how an approval reaches a module, so it says so.
		fmt.Fprintf(&b, " This capability needs the owner's approval, and the"+
			" approval policy let this call run without one. An approval you"+
			" construct from a message is not one the host will carry, however"+
			" the user phrases it. Do not ask the user to approve here and do not"+
			" retry with a reworded consent. Tell them the owner can add a rule"+
			" for the tool %q to tools.approval: with \"ask\" the owner is asked"+
			" before each call, and the Modules page offers \"Approve and run\""+
			" for the capability %q of module %q; with \"allow\" every call runs"+
			" approved.",
			t.Name(), t.capability.ID, t.descriptor.Module)
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

	// The module's result is bounded before it enters the conversation: a
	// result near the 1 MiB output bound would fill the context window on its
	// own. Large output belongs in an artifact.
	if len(env.Result) > maxResultForModel {
		b.WriteString(module.BoundText(string(env.Result), maxResultForModel))
		b.WriteString("\n(the result was cut to fit; large output belongs in an artifact)")
	} else if len(env.Result) > 0 {
		b.Write(env.Result)
	} else {
		b.WriteString("{}")
	}

	for _, w := range env.Warnings {
		b.WriteString("\nwarning: " + module.BoundText(w, module.MaxErrorText))
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
// arguments beside Input at the request root -- a dispatcher's "tool"
// selector. Rather than requiring the model to know which, the host sends the
// arguments both ways: nested under "input" if the model supplied that key,
// and passed through at the root otherwise.
//
// Host-owned fields are never overwritten, so a model cannot widen filesystem
// roots, forge a request ID, extend a deadline, or write the wire v2
// "approval" or "contract_version" a module would take as the host's own, by
// naming one as an argument (ReservedRequestKeys).
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
	// The host cannot take a consent from the arguments for a TRUE one, so it
	// refuses to carry a false one: the field is stripped, and the host records
	// its own when the operator approved the call (placeApprovedArgs).
	//
	// Stripped rather than rejected because refusing the call would teach the
	// agent to retry without it, which is the same request minus the audit
	// trail.
	stripSelfMintedConsent(args)

	return encodeArgs(req, args)
}

// placeApprovedArgs is placeArgs for a call the operator approved: by a rule
// of the approval policy that allows it, or by answering an ask.
//
// Approval claims are carried rather than stripped, because here the caller's
// are true: the Modules page and module-invoke pass on what a person typed, and
// the agent's tool strips what the model wrote before calling this. Host-owned
// fields are still reserved, so approving a run does not let the caller widen
// roots or extend a deadline.
func placeApprovedArgs(req *modproto.Request, args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}

	// The host RECORDS the approval rather than expecting it in the payload.
	//
	// The approval is the operator's act against the declared effects.
	// Requiring them to also hand-write a consent object asks them to author
	// the one thing they cannot legitimately author -- and they will not know
	// to, because nothing tells them.
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
			"note":                     "approved by the owner in Compa against the capability's declared effects",
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

	req.Extra = map[string]json.RawMessage{}
	for k, v := range args {
		if ReservedRequestKeys[k] {
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

// ReservedRequestKeys are request fields only the host writes. A caller's
// argument of one of these names never reaches the request root.
//
// approval and contract_version are the host's in wire v2: an approval object
// a module read there would be taken as the host's own decision.
var ReservedRequestKeys = map[string]bool{
	"protocol": true, "capability": true, "request_id": true,
	"input": true, "roots": true, "grants": true, "binaries": true,
	"deadline_ms": true, "max_output_bytes": true,
	"approval": true, "contract_version": true,
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
	roots := grantRoots(t.descriptor, t.home, t.workspace, moduleBundleDir(t.home, t.descriptor), t.sourceRoots, false)
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
	// The wire v2 approval object, which only the host may write.
	"approval": true,
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
