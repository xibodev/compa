// Package modproto defines the Compa detached-module protocol: the
// wire contract between the host and locally installed module executables.
//
// The host speaks exactly two verbs to a module binary:
//
//	<module-bin> module describe --json
//	<module-bin> module invoke <capability> --input <request.json>
//
// There is no legacy fallback shape and no per-module adapter branch. A module
// keeps whatever human-facing CLI it likes; the host never calls it.
//
// Wire rules (enforced by the host, not merely documented):
//
//   - stdout carries exactly one bounded JSON envelope and nothing else;
//   - stderr is advisory only: captured, bounded, surfaced as diagnostics,
//     and never parsed for control flow or protocol data;
//   - modules are detached local processes, never imported Go packages;
//   - every path is explicit, canonicalized, and confined to a declared root;
//   - unknown cost stays unknown; it never silently becomes zero;
//   - module output is untrusted input until the host validates it.
//
// # Empty collections
//
// Every slice and map field is emitted as [] or {} and never as null, so no
// reader has to distinguish "absent" from "empty". Go does not give this for
// free: json.Marshal renders a nil slice as null and a nil map as null.
//
// Order matters, and getting it wrong is silent. Envelope.Result is a
// json.RawMessage, so a payload is already opaque bytes by the time the
// envelope is normalized; normalizing only the envelope fixes Warnings and
// Artifacts while leaving the payload itself full of nulls. The payload must be
// normalized FIRST, then marshalled into Result, then the envelope normalized.
//
// Build responses with NewResultEnvelope, NewErrorEnvelope, or
// NewDescribeEnvelope, which do this in the right order. They exist so that
// correctness does not depend on remembering the order.
//
// # Digests
//
// Every digest field in this protocol is "sha256:" followed by lowercase hex.
// The algorithm prefix is mandatory so the protocol can adopt another hash
// without ambiguity. Use DigestSHA256 to format one.
package modproto

import "encoding/json"

// ProtocolID is the value modules must report in Envelope.Protocol and list in
// Descriptor.ProtocolVersions.
//
// FROZEN by operator decision (2026-09-07), confirmed directly to this lane and
// relayed independently through one of the module lanes.
//
// The value is deliberately vendor-neutral rather than host-named. It is the
// one identifier every lane shares, and every module is as much a first-class
// participant as any other; "xibodev." is the cross-lane namespace root, just
// as a module-owned contract such as the example.archive.seed/v1 seed contract
// is namespaced by its module and never by the host. Host identity (the compa
// binary, COMPA_HOME, the agent) is host-owned and unaffected.
const ProtocolID = "xibodev.module/v1"

// The two verbs the host speaks, and the only legal values of
// Envelope.Operation.
//
// Operation names the VERB, never the capability. The capability travels in
// Request.Capability and is correlated by request_id; duplicating it here would
// let "operation" mean different things depending on which module answered.
const (
	OperationDescribe = "describe"
	OperationInvoke   = "invoke"
)

// ---------------------------------------------------------------------------
// Discovery: `module describe --json`
// ---------------------------------------------------------------------------

// Descriptor is the complete result of `module describe --json`.
//
// The field set is closed. All three lane prompts (the host and two modules)
// independently specify this exact list, so it is treated as pre-agreed. Fields
// are added only by a protocol version bump acknowledged by every lane.
//
// Slice and map fields are always emitted as [] or {} and never as null, so the
// host never has to distinguish "absent" from "empty".
type Descriptor struct {
	// Module is the stable module ID, e.g. "render" or "archive". It is the
	// directory name under $COMPA_HOME/modules/ and the namespace
	// prefix for every capability the module offers.
	Module string `json:"module"`

	// Name is the human-readable display name for the cockpit.
	Name string `json:"name"`

	// Version is the module's own release version, independent of the
	// protocol version.
	Version string `json:"version"`

	// ProtocolVersions lists every protocol ID this binary can speak.
	//
	// The host requires ProtocolID to appear in this list and refuses the
	// module otherwise (validate.go). That is a MEMBERSHIP TEST against one
	// frozen constant -- it is NOT negotiation. There is no ordering, no
	// selection, and no "highest common version": declaring any ID other than
	// ProtocolID has no effect at all.
	//
	// This comment previously described the host as picking the greatest
	// mutually supported version. It never did. A sibling lane inferred a
	// versioning story from that sentence and had to retract it, which is why
	// the wording here now describes the membership test and nothing more.
	//
	// Real negotiation is a successor-contract concern. It must not be added
	// to v1 to make an old comment retroactively true.
	ProtocolVersions []string `json:"protocol_versions"`

	// Capabilities are the invocable operations, keyed by capability ID.
	Capabilities []Capability `json:"capabilities"`

	// RequestSchemas and ResultSchemas map a schema ID to a JSON Schema
	// document. Capabilities reference these by ID rather than inlining them,
	// so shared shapes are declared once.
	RequestSchemas map[string]json.RawMessage `json:"request_schemas"`
	ResultSchemas  map[string]json.RawMessage `json:"result_schemas"`

	// ArtifactSchemas maps an artifact schema ID to its JSON Schema.
	//
	// The host checks that every ID a capability lists in its own
	// ArtifactSchemas is present in this map (validate.go), and nothing more.
	// PRODUCED ARTIFACTS ARE NOT VALIDATED AGAINST THESE SCHEMAS. Grep for
	// ArtifactSchemas in internal/view, internal/moduletools and web/backend:
	// there are no readers. Rendering is decided by media type and the
	// module's presentation hint (internal/view.Resolve), never by a schema.
	//
	// This comment previously described the host as checking produced
	// artifacts against these schemas before rendering a card. It does not,
	// and a sibling lane was repairing its declarations toward that guarantee
	// before being told.
	//
	// Two consequences worth stating for the successor contract rather than
	// fixing here: a declared-but-unread field is indistinguishable from an
	// enforced one to anyone reading the type, and most artifacts in practice
	// are MEDIA (mp4, mp3, pdf) which a JSON Schema cannot validate at all.
	ArtifactSchemas map[string]json.RawMessage `json:"artifact_schemas"`

	// AgentOverlays are concise module-authored instruction documents the host
	// may compose into the agent's prompt. The host decides which to load and
	// when; a module cannot force its overlay into every turn.
	AgentOverlays []Overlay `json:"agent_overlays"`

	// Skills are progressively loadable module knowledge. The host loads only
	// those selected for the current request.
	Skills []Skill `json:"skills"`

	// Permissions is what the module declares it may need. It is a REQUEST,
	// never a grant: installing a module grants nothing. The host intersects
	// this with its own policy and authorizes per invocation.
	Permissions Permissions `json:"permissions"`

	// Requirements are external preconditions (binaries, versions, config)
	// that the host surfaces to the user when unmet.
	Requirements []Requirement `json:"requirements"`
}

// Capability is one invocable operation.
type Capability struct {
	// ID is the capability identifier passed to `module invoke`. It is
	// namespaced by module, e.g. "creative.tools.run", "sessions.assay".
	ID string `json:"id"`

	// Title and Summary are cockpit-facing copy. Summary should be one line:
	// it is what the host folds into the agent's capability list, so every
	// enabled capability costs roughly one line of context.
	Title   string `json:"title"`
	Summary string `json:"summary"`

	// RequestSchema and ResultSchema are IDs into the descriptor's schema maps.
	//
	// RequestSchema describes the contents of Request.Input, NOT the whole
	// Request. The envelope fields around it -- protocol, capability,
	// request_id, roots, grants, binaries, deadline_ms -- are the host's and are
	// fixed by this package; a capability never redeclares them.
	//
	// This was under-specified in the first draft, which said only "IDs into the
	// descriptor's schema maps". Two lanes then read it differently and both
	// were reasonable: one described Input, the other described the whole
	// Request with `tool` and `input` as siblings. A host building a request
	// from the schema got rejected by a module whose own reader disagreed with
	// its own declaration -- a class invisible to schema checks and unit tests
	// alike, because each side was self-consistent.
	RequestSchema string `json:"request_schema"`
	ResultSchema  string `json:"result_schema"`

	// ArtifactSchemas lists artifact schema IDs this capability may produce.
	ArtifactSchemas []string `json:"artifact_schemas"`

	// Effects declares what invoking this capability does, before it is run.
	// The host uses it to decide whether approval is required, so it must be
	// honest even when the answer is inconvenient.
	Effects Effects `json:"effects"`

	// Skills lists skill IDs relevant to this capability, letting the host
	// load knowledge lazily and only for the capability actually in play.
	Skills []string `json:"skills"`

	// LongRunning marks a capability that may return a job handle in its
	// result rather than a completed outcome. When true, PollCapability names
	// the capability that polls it.
	LongRunning    bool   `json:"long_running"`
	PollCapability string `json:"poll_capability,omitempty"`
}

// Effects is the pre-declared effect profile of a capability. It mirrors
// Execution, which reports what an invocation ACTUALLY did.
type Effects struct {
	Local          bool   `json:"local"`
	Network        bool   `json:"network"`
	ExternalWrites bool   `json:"external_writes"`
	Provider       string `json:"provider"`

	// CostKnown declares whether a NUMERIC MONETARY COST IS KNOWN for this
	// capability. That is its whole meaning: "is there a number". It does NOT
	// mean "this may spend money", and the two are independent -- a capability
	// can reach a paid provider at a price it knows exactly, and another can be
	// free with no number to give.
	//
	// The distinction was settled by the lane that owns the field, by running
	// rather than reading: one of its tools declares cost_known TRUE and
	// network TRUE and requires no consent at all.
	//
	// SEPARATELY, and as a HOST POLICY rather than a property of this field:
	// Compa currently refuses a capability with cost_known false unless
	// the operator approved it (internal/moduletools.NeedsApproval). That is
	// this host over-gating on the only signal v1 carries, not a guarantee the
	// protocol makes. Another host may gate differently; one sibling does.
	//
	// v1 has no way to say "may incur a monetary charge" independent of
	// whether the amount is known. That is a successor-contract concern and
	// must not be smuggled into v1.
	CostKnown bool `json:"cost_known"`
}

// ProviderVaries is the declared-provider value meaning "not knowable until
// dispatch".
//
// NOT A NEW v1 FIELD AND NOT A CONTRACT CHANGE. Effects.Provider is a free
// string and always was; this names a value that was already being used, so
// the host and a module stop spelling it independently. v1 is immutable in its
// SHAPE -- nothing is added here, and a module that never uses this word is
// unaffected.
//
// WHY IT EXISTS. A capability that dispatches many tools cannot name one
// provider at describe time, because the description is read BEFORE the
// request selects which. A sibling lane's creative.tools.run reaches 35 tools
// across twelve providers. Declaring any single one would be false; declaring
// "" would waive the check entirely for capabilities that CAN name theirs.
//
// The host exempts exactly this token on the DECLARED side and nothing else,
// so it cannot become a general escape hatch: a capability that genuinely
// reaches one provider still has to match, and silencing the check would take
// declaring this exact word -- a visible statement rather than an accident.
const ProviderVaries = "varies"

// Overlay is a module-authored agent instruction document.
type Overlay struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Path is relative to the module root and is resolved and confined by the
	// host. A module never hands the host an absolute path.
	Path string `json:"path"`
	// Digest is "sha256:<lowercase-hex>" over the file contents. The host
	// records it as provenance for anything folded into agent context and
	// refuses content whose digest does not match.
	Digest string `json:"digest"`
	// Tokens is the module's estimate of the overlay's context cost, so the
	// host can budget before loading it.
	Tokens int `json:"tokens"`
}

// Skill is progressively loadable module knowledge.
type Skill struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`
	Path    string `json:"path"`
	// Digest is "sha256:<lowercase-hex>" over the file contents.
	Digest string `json:"digest"`
	// Tokens is the module's estimate of the skill's context cost.
	Tokens int `json:"tokens"`
}

// Permissions is what a module declares it may need. Every field defaults to
// the closed position, so a module that omits this block gets nothing.
type Permissions struct {
	// FilesystemRead and FilesystemWrite are logical root NAMES the module
	// expects the host to supply, not paths. The module never resolves a root
	// itself; the host passes canonicalized absolute paths per invocation.
	FilesystemRead  []string `json:"filesystem_read"`
	FilesystemWrite []string `json:"filesystem_write"`

	// Network, Credentials, PaidProviders, Publish and Subprocess each require
	// explicit host policy plus, where configured, human approval.
	//
	// Subprocess is deliberately distinct from Network: a module that shells
	// out to an already-authenticated CLI needs subprocess authority, not
	// network or credential authority. Modelling that as "network" would be
	// both wrong and insufficient.
	Network       []string `json:"network"`
	Credentials   []string `json:"credentials"`
	PaidProviders []string `json:"paid_providers"`
	Publish       bool     `json:"publish"`
	Subprocess    []string `json:"subprocess"`
}

// Requirement is an external precondition the host surfaces when unmet.
type Requirement struct {
	// Kind is "binary", "version", "config", or "env".
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Available is the module's own probe result. The host displays it and
	// does not re-probe.
	Available bool `json:"available"`
	// Detail explains what is missing and how to satisfy it. This is the field
	// that makes "Needs setup" actionable, so it should name the dependency.
	Detail string `json:"detail,omitempty"`
}

// ---------------------------------------------------------------------------
// Invocation: `module invoke <capability> --input <request.json>`
// ---------------------------------------------------------------------------

// Request is the JSON document the host writes and passes via --input.
//
// The host always supplies RequestID and Roots. A module must not resolve paths
// from the environment, the working directory, or its own configuration: every
// path it may touch arrives here, already canonicalized and absolute, so the
// host can enforce confinement rather than trust the module.
type Request struct {
	Protocol   string `json:"protocol"`
	Capability string `json:"capability"`
	RequestID  string `json:"request_id"`
	// Input is the capability-specific payload, and is the ONLY part of the
	// request a capability's declared RequestSchema describes. Everything
	// around it belongs to the host.
	Input json.RawMessage `json:"input"`

	// Roots maps logical root names declared in Permissions to canonicalized
	// absolute paths, each with an explicit access mode.
	Roots map[string]Root `json:"roots"`

	// Grants are the permissions actually authorized for THIS invocation.
	// A module must assume it has nothing that is not listed here.
	Grants Grants `json:"grants"`

	// Binaries maps a declared subprocess name to the ABSOLUTE path the host
	// resolved for it, keyed by the same names used in Permissions.Subprocess.
	//
	// Modules run with no inherited environment, so there is no PATH to search.
	// The host resolves and supplies paths instead, which is stronger than
	// supplying a minimal PATH: a search can find something that was never
	// declared -- a shadowing entry, or a second binary of the same name -- so
	// a path is an identity while a PATH is a query. It also makes
	// Permissions.Subprocess load-bearing rather than documentary: a binary the
	// module did not declare is never supplied, so it cannot be run.
	//
	// A declared binary the host could not resolve is ABSENT from this map
	// rather than present-and-empty, so "not supplied" and "supplied as
	// nothing" cannot be confused. Modules must fail closed with
	// ErrMissingRequirement rather than falling back to a lookup of their own.
	Binaries map[string]string `json:"binaries"`

	// DeadlineMS is the host's wall-clock budget. The module should aim to
	// return a bounded envelope before it elapses; the host enforces it by
	// killing the process tree regardless.
	DeadlineMS int `json:"deadline_ms"`

	// MaxOutputBytes is the stdout ceiling. Output beyond it is truncated and
	// the invocation fails as a protocol violation, so large results belong in
	// an artifact pointer rather than inline in the envelope.
	MaxOutputBytes int `json:"max_output_bytes"`

	// Extra carries additional root-level fields for modules that read some of
	// their arguments beside Input rather than inside it.
	//
	// Input remains the contract that RequestSchema describes. Extra exists
	// because real modules were already built the other way and a v1 that
	// cannot drive them is not a working v1. It is marshalled INLINE at the
	// request root, and host-owned fields always win, so a module cannot
	// capture protocol, roots, grants, or the bounds by naming them here.
	Extra map[string]json.RawMessage `json:"-"`
}

// MarshalJSON writes the request with Extra flattened into the root object.
//
// Host-owned keys are written last and unconditionally, so an Extra entry can
// never shadow one. That ordering is the security property: without it, a
// module could widen its own roots or bounds by naming them.
func (r Request) MarshalJSON() ([]byte, error) {
	type plain Request // avoid recursing into this method
	base, err := json.Marshal(plain(r))
	if err != nil {
		return nil, err
	}

	if len(r.Extra) == 0 {
		return base, nil
	}

	var merged map[string]json.RawMessage
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range r.Extra {
		if _, reserved := merged[k]; reserved {
			continue // host-owned fields always win
		}
		merged[k] = v
	}
	return json.Marshal(merged)
}

// Root is one canonicalized filesystem root with its access mode.
type Root struct {
	Path string `json:"path"`
	// Mode is "ro" or "rw". A read-only root is the host's guarantee that a
	// source store cannot be mutated, independent of module behaviour.
	Mode string `json:"mode"`
}

// Grants is the per-invocation authorization set. Install-time grants nothing;
// this is where authority actually comes from.
type Grants struct {
	Network       []string `json:"network"`
	Credentials   []string `json:"credentials"`
	PaidProviders []string `json:"paid_providers"`
	Publish       bool     `json:"publish"`
	Subprocess    []string `json:"subprocess"`
}

// Envelope is the single JSON document a module writes to stdout, for both
// describe and invoke. Nothing else may appear on stdout.
type Envelope struct {
	Protocol string `json:"protocol"`
	Module   string `json:"module"`
	// Operation is the VERB: exactly OperationDescribe or OperationInvoke.
	// It is not the capability ID.
	Operation string `json:"operation"`

	// RequestID is host-generated and echoed back VERBATIM, so the host can
	// correlate without trusting the module to be unique. For describe, it is
	// empty.
	RequestID string `json:"request_id"`

	OK bool `json:"ok"`

	// Result is present when OK; Error is present when not. Exactly one of the
	// two appears.
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`

	// Warnings is always an array, never null.
	Warnings []string `json:"warnings"`

	// Execution reports what the invocation ACTUALLY did, and is present even
	// on failure, because a failed paid call may still have cost money.
	Execution Execution `json:"execution"`
}

// Error is a structured, bounded failure.
type Error struct {
	// Code is stable and machine-readable; the host routes on it and never on
	// Message text.
	Code string `json:"code"`
	// Message is human-readable and safe to show in the cockpit.
	Message string `json:"message"`
	// Retryable tells the host whether retrying unchanged could succeed.
	Retryable bool `json:"retryable"`
	// Details is bounded structured context. It must never carry credentials,
	// raw private transcripts, or internal database paths.
	Details map[string]any `json:"details"`
}

// Execution is what actually happened. The field set is closed and identical
// across all three lane prompts.
type Execution struct {
	Local          bool   `json:"local"`
	Network        bool   `json:"network"`
	ExternalWrites bool   `json:"external_writes"`
	Provider       string `json:"provider"`

	// EstimatedCost and ActualCost are POINTERS on purpose.
	//
	// null means genuinely unknown. 0 means genuinely free. Collapsing the two
	// would let an unpriced provider call render as free in the cockpit and
	// slip past cost approval, so the host treats null as "requires approval"
	// and never as zero.
	EstimatedCost *float64 `json:"estimated_cost"`
	ActualCost    *float64 `json:"actual_cost"`

	// Artifacts are pointers to produced files, never inline payloads. Large
	// results belong here so the envelope stays bounded.
	Artifacts []Artifact `json:"artifacts"`
}

// Artifact is a pointer to something a module produced.
type Artifact struct {
	ID string `json:"id"`
	// Kind is the artifact schema ID, matching a key in the descriptor's
	// ArtifactSchemas. It selects the cockpit renderer.
	Kind string `json:"kind"`
	// Path is relative to a declared root; Root names which one, using a root
	// name the HOST supplied to the PRODUCING module. The host re-validates
	// confinement before touching the file.
	//
	// Root names are therefore scoped to one invocation and are not portable
	// between modules: a seed that one module writes under its own rw root
	// cannot be named by a root another module was given. Cross-module handoff
	// is resolved by the host STAGING the artifact into its own artifact store
	// and supplying that as a read-only root to the consumer. Modules never
	// learn each other's layout, and the host keeps a single enforcement point.
	Path string `json:"path"`
	Root string `json:"root"`
	// Presentation names the rendering primitive the host should use, when the
	// module knows better than the media type does.
	//
	// A JSON document may be a timeline; a set of images may be a QA frame grid
	// that is useless viewed one at a time. Only the module knows that, so it
	// may say -- but it names a primitive the HOST already owns and never
	// supplies view code. An unknown value is ignored rather than honoured, so
	// a module cannot invent a renderer, and the media type decides when the
	// hint is absent.
	//
	// Values: video, audio, image, image_grid, timeline, document, diagram,
	// slides, table, markdown, text, json, download.
	Presentation string `json:"presentation,omitempty"`

	// MediaType, Bytes and Digest let the host render, budget, and verify
	// without opening the file first. Digest is "sha256:<lowercase-hex>".
	MediaType string `json:"media_type"`
	Bytes     int64  `json:"bytes"`
	Digest    string `json:"digest"`
	Title     string `json:"title,omitempty"`
}

// ---------------------------------------------------------------------------
// Stable error codes
// ---------------------------------------------------------------------------

// Codes a module may return. The host routes on these; unrecognized codes are
// treated as non-retryable module failures.
const (
	ErrUnsupportedProtocol = "unsupported_protocol"
	ErrUnknownCapability   = "unknown_capability"
	ErrInvalidRequest      = "invalid_request"
	ErrMissingRequirement  = "missing_requirement"
	ErrPermissionDenied    = "permission_denied"
	ErrPathOutsideRoot     = "path_outside_root"
	ErrProviderFailure     = "provider_failure"
	ErrTimeout             = "timeout"
	ErrCancelled           = "cancelled"
	ErrInternal            = "internal"
)

// Codes the HOST generates when a module misbehaves. A module never emits
// these; they describe failures of the module contract itself.
const (
	ErrHostProtocolViolation = "host.protocol_violation"
	ErrHostOutputTooLarge    = "host.output_too_large"
	ErrHostTimeout           = "host.timeout"
	ErrHostSpawnFailed       = "host.spawn_failed"
	ErrHostInvalidJSON       = "host.invalid_json"
)
