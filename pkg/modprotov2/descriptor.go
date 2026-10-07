// Package modprotov2 is the wire representation of the frozen
// xibodev.module/v2 behavioural contract.
//
// SEPARATE PACKAGE FROM modproto, DELIBERATELY. modproto is xibodev.module/v1:
// immutable legacy behaviour that nothing may be added to. A module speaks one
// wire or the other, never both, and putting v2 fields on v1 structs would be
// the "smuggling successor semantics into a v1 exchange" that final ruling 5
// forbids -- tolerant decoding is not permission.
//
// WHAT THIS PACKAGE IS AND IS NOT. It carries the frozen shape and the
// mechanical checks the contract requires. It does NOT decide policy: whether
// a host gates on a given effect, how it renders an artifact, or what it does
// with a refusal, all live above this package. The contract says what may be
// said; the host says what to do about it.
//
// EVERY FIELD TRACES TO A FROZEN CLAUSE. `long_running` was in the draft, is
// not in the frozen contract, and is cut. It remains in v1 on
// modproto.Capability.LongRunning, which is untouched.
package modprotov2

import "encoding/json"

// ContractID is the behavioural contract this package implements. It is
// deliberately duplicated nowhere: pkg/contractv2 owns the pin check and this
// package's Descriptor carries the declared value, but the constant itself
// lives in contractv2 so a drift is impossible.

// Descriptor is what `module describe --json` returns under v2.
//
// TWO LAYERS, AND THE PROJECTION IS NOT 1:1 IN EITHER DIRECTION (§2).
// Operations are semantic units derived by contract; Capabilities are what a
// host actually invokes. One Operation may surface through several
// capabilities, and one capability may reach many Operations -- a rendering
// module's creative.tools.run reaches any of 35. A capability may also project
// NONE: a registry read transforms no product material and is still
// invocable, so it still declares effects.
type Descriptor struct {
	// Protocol is the WIRE format identity. ContractVersion is the BEHAVIOURAL
	// contract. Conflating them is the axis error that made v1's version list
	// look like negotiation, so both are carried and neither is derived from
	// the other.
	Protocol        string `json:"protocol"`
	ContractVersion string `json:"contract_version"`

	Module  string `json:"module"`
	Name    string `json:"name"`
	Version string `json:"version"`

	// Operations are LAYER 1: canonical semantic units. Their effects are the
	// product truth a capability's declaration must not weaken (§2, §9).
	//
	// v1 has no equivalent at all -- Capability was the only unit -- so the
	// no-weakening check had nothing to compare against. That absence is the
	// gap this field closes.
	Operations []Operation `json:"operations"`

	// Capabilities are LAYER 2: what a host invokes. The gate reads THESE
	// effects (§2a rule 2), which is why they must be no weaker than every
	// Operation projected.
	Capabilities []Capability `json:"capabilities"`

	// ArtifactKinds declares produced artifacts BY KIND (§7). Not all JSON:
	// v1's artifact_schemas implied a JSON Schema for everything and validated
	// nothing, which meant an mp4 was undeclarable without implying a
	// validator that cannot exist.
	ArtifactKinds map[string]ArtifactKind `json:"artifact_kinds"`

	RequestSchemas map[string]json.RawMessage `json:"request_schemas"`
	ResultSchemas  map[string]json.RawMessage `json:"result_schemas"`

	Permissions   Permissions `json:"permissions"`
	AgentOverlays []Overlay   `json:"agent_overlays"`
	Skills        []Skill     `json:"skills"`
}

// Operation is a canonical semantic unit, NOT a delivery surface (final ruling
// 10). It is derived by contract from what the product means, so a module with
// one Operation reachable four ways declares one Operation and four
// capabilities.
type Operation struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`

	Effects   Effects   `json:"effects"`
	Execution ExecProps `json:"execution"`

	// Requirements carry STRENGTH (§5). v1 could not say whether a missing
	// dependency was fatal or merely degrading.
	Requirements []Requirement `json:"requirements"`

	// Approval carries a REASON (§6). v1 gated on one field, so a
	// free-but-irreversible step was ungated entirely.
	Approval Approval `json:"approval"`

	// Produces names artifact kinds by key into Descriptor.ArtifactKinds.
	Produces []string `json:"produces"`
}

// Capability is an invocable surface.
type Capability struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Summary string `json:"summary"`

	RequestSchema string `json:"request_schema"`
	ResultSchema  string `json:"result_schema"`

	// Projects names the Operations this capability surfaces. EMPTY IS LEGAL
	// and is not a defect -- see the type doc.
	Projects []string `json:"projects"`

	// Effects are declared SEPARATELY from the projected Operations' effects,
	// in the SAME SHAPE, so the no-weakening comparison is mechanical rather
	// than a translation. A translation is where a weakening hides.
	Effects Effects `json:"effects"`
}

// ExecProps bounds an invocation.
//
// `long_running` is NOT here. It was in the draft and is cut: it appears zero
// times in the frozen RFC, and it told a host THAT something takes a while
// while giving it nothing to do about it -- no handle, no poll verb, no resume,
// all out of scope. A rendering module's poll_scope gap is real (a host
// polling an expired in-memory handle gets unknown_job, indistinguishable from
// a job that never existed) and is recorded as the first successor item rather
// than smuggled in as a field for an object v2 cannot express.
type ExecProps struct {
	// DeadlineMSDefault bounds the WHOLE invocation, wall-clock, from process
	// start (§8).
	DeadlineMSDefault int `json:"deadline_ms_default"`
}

// Approval says WHY approval is required, not merely that it is (§6).
type Approval struct {
	// RequiredWhen holds reasons from the legal set: "chargeable",
	// "irreversible", "external_write", "product_checkpoint".
	RequiredWhen []string `json:"required_when"`
}

// Permissions is unchanged from v1 in meaning: roots, grants and binaries work
// and §8 states them normatively, so v2 changes nothing about them.
type Permissions struct {
	FilesystemRead  []string `json:"filesystem_read"`
	FilesystemWrite []string `json:"filesystem_write"`
	Network         []string `json:"network"`
	Credentials     []string `json:"credentials"`
	PaidProviders   []string `json:"paid_providers"`
	Publish         bool     `json:"publish"`
	Subprocess      []string `json:"subprocess"`
}

// Overlay and Skill are unchanged from v1.
type Overlay struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type Skill struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
