package modproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// DigestPrefix is the mandatory algorithm prefix on every digest field in this
// protocol. The prefix is required so a future hash change is unambiguous
// rather than a silent reinterpretation of existing bare hex.
const DigestPrefix = "sha256:"

// DigestSHA256 formats content as a protocol digest: "sha256:<lowercase-hex>".
func DigestSHA256(content []byte) string {
	sum := sha256.Sum256(content)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// ValidDigest reports whether s is a well-formed protocol digest. It checks
// shape only; it does not verify the digest against any content.
func ValidDigest(s string) bool {
	rest, ok := strings.CutPrefix(s, DigestPrefix)
	if !ok || len(rest) != sha256.Size*2 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		isLowerHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')
		if !isLowerHex {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Empty-collection normalization
// ---------------------------------------------------------------------------
//
// Go marshals a nil slice as null and a nil map as null. The protocol requires
// [] and {}, so every collection must be non-nil before marshalling. Doing this
// in one shared place means a module cannot ship nulls by forgetting a guard,
// and the host does not have to accept both shapes.

func emptySlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func emptyRawMap[T any](m map[string]T) map[string]T {
	if m == nil {
		return map[string]T{}
	}
	return m
}

// Normalize replaces every nil collection in the descriptor with an empty one,
// recursively. Call it immediately before marshalling a describe response.
func (d *Descriptor) Normalize() {
	d.ProtocolVersions = emptySlice(d.ProtocolVersions)
	d.Capabilities = emptySlice(d.Capabilities)
	d.RequestSchemas = emptyRawMap(d.RequestSchemas)
	d.ResultSchemas = emptyRawMap(d.ResultSchemas)
	d.ArtifactSchemas = emptyRawMap(d.ArtifactSchemas)
	d.AgentOverlays = emptySlice(d.AgentOverlays)
	d.Skills = emptySlice(d.Skills)
	d.Requirements = emptySlice(d.Requirements)

	for i := range d.Capabilities {
		d.Capabilities[i].ArtifactSchemas = emptySlice(d.Capabilities[i].ArtifactSchemas)
		d.Capabilities[i].Skills = emptySlice(d.Capabilities[i].Skills)
	}

	d.Permissions.Normalize()
}

// Normalize replaces every nil slice in the permission set with an empty one.
func (p *Permissions) Normalize() {
	p.FilesystemRead = emptySlice(p.FilesystemRead)
	p.FilesystemWrite = emptySlice(p.FilesystemWrite)
	p.Network = emptySlice(p.Network)
	p.Credentials = emptySlice(p.Credentials)
	p.PaidProviders = emptySlice(p.PaidProviders)
	p.Subprocess = emptySlice(p.Subprocess)
}

// Normalize replaces every nil slice in the grant set with an empty one.
func (g *Grants) Normalize() {
	g.Network = emptySlice(g.Network)
	g.Credentials = emptySlice(g.Credentials)
	g.PaidProviders = emptySlice(g.PaidProviders)
	g.Subprocess = emptySlice(g.Subprocess)
}

// Normalize replaces every nil collection in the envelope with an empty one,
// recursively. Call it immediately before marshalling any response.
//
// It deliberately does NOT touch EstimatedCost or ActualCost: a nil cost is
// meaningful (unknown) and must never be normalized into zero.
func (e *Envelope) Normalize() {
	e.Warnings = emptySlice(e.Warnings)
	e.Execution.Artifacts = emptySlice(e.Execution.Artifacts)
	if e.Error != nil && e.Error.Details == nil {
		e.Error.Details = map[string]any{}
	}
}

// Normalize replaces every nil collection in the request with an empty one.
func (r *Request) Normalize() {
	if r.Roots == nil {
		r.Roots = map[string]Root{}
	}
	if r.Binaries == nil {
		r.Binaries = map[string]string{}
	}
	r.Grants.Normalize()
}

// ---------------------------------------------------------------------------
// Envelope construction
// ---------------------------------------------------------------------------
//
// Envelope.Result is json.RawMessage, so a payload is already opaque bytes by
// the time Normalize runs on the envelope. Normalizing the envelope alone
// therefore fixes Warnings and Artifacts while leaving the actual payload full
// of nulls -- the natural single call produces a subtly wrong document.
//
// The constructors below close that gap by normalizing the payload FIRST and
// marshalling it into Result, so callers cannot get the order wrong. Prefer
// them over assembling an Envelope by hand.

// Normalizer is implemented by protocol payloads that carry collections needing
// empty-not-null treatment. Descriptor implements it; capability result types
// in any lane may implement it too, and NewResultEnvelope will honour it.
type Normalizer interface {
	Normalize()
}

// NewResultEnvelope builds a successful envelope around payload, normalizing in
// the correct order: payload first, then the envelope.
//
// If payload implements Normalizer its Normalize method is called before
// marshalling, which is what keeps nested collections from reaching the wire as
// null. Pass a pointer when the payload's Normalize has a pointer receiver, as
// Descriptor's does.
//
// requestID is echoed verbatim from the request; pass "" for describe.
func NewResultEnvelope(module, operation, requestID string, payload any, exec Execution) (Envelope, error) {
	if n, ok := payload.(Normalizer); ok {
		n.Normalize()
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}

	env := Envelope{
		Protocol:  ProtocolID,
		Module:    module,
		Operation: operation,
		RequestID: requestID,
		OK:        true,
		Result:    raw,
		Execution: exec,
	}
	env.Normalize()
	return env, nil
}

// NewErrorEnvelope builds a failed envelope.
//
// Execution is required rather than optional because a failed invocation may
// still have cost money or written something, and the host needs that reported
// honestly. Pass a zero-value Execution only when the failure genuinely had no
// effects; note that leaves both cost pointers nil, meaning "unknown", which is
// the safe default.
func NewErrorEnvelope(module, operation, requestID string, e Error, exec Execution) Envelope {
	env := Envelope{
		Protocol:  ProtocolID,
		Module:    module,
		Operation: operation,
		RequestID: requestID,
		OK:        false,
		Error:     &e,
		Execution: exec,
	}
	env.Normalize()
	return env
}

// NewDescribeEnvelope builds the response to `module describe --json`.
//
// Discovery is local, does no network, writes nothing, and is free, so its
// Execution says exactly that -- with costs as explicit zeroes rather than nil,
// because describe is genuinely free rather than of unknown price.
func NewDescribeEnvelope(d *Descriptor) (Envelope, error) {
	free := 0.0
	return NewResultEnvelope(d.Module, OperationDescribe, "", d, Execution{
		Local:         true,
		Provider:      "local",
		EstimatedCost: &free,
		ActualCost:    &free,
	})
}
