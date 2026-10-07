package modprotov2

// Kind classifies an artifact by what CAN be checked about it (§7).
//
// THE GAP THIS CLOSES. v1's artifact_schemas mapped every artifact to a JSON
// Schema and validated none of them. That is wrong in two directions at once:
// nothing was enforced, and an mp4 was undeclarable without implying a
// validator that cannot exist. A rendering module raised the second; it is
// what created §7.
type Kind string

const (
	// KindDocument has a validator that validates the artifact's OWN CONTENT.
	KindDocument Kind = "document"

	// KindText has no validation contract beyond media type and digest. This
	// is an HONEST statement that no validator exists, not a gap -- a notes
	// module's markdown artifacts are exactly this today.
	KindText Kind = "text"

	// KindMedia is validated by media type, size and digest. NEVER by JSON
	// Schema. Naming a validator here is an ERROR rather than an omission,
	// because a JSON Schema that "validates" an mp4 is a claim nothing can
	// honour.
	KindMedia Kind = "media"
)

// ArtifactKind declares one produced artifact type.
type ArtifactKind struct {
	Kind      Kind       `json:"kind"`
	MediaType string     `json:"media_type"`
	Validator *Validator `json:"validator,omitempty"`
}

// Validator names how a document artifact's CONTENT is checked.
//
// "Names a validator that RESOLVES" is not the bar, and that weaker wording
// shipped here first. One module declared application/json as a document
// naming a real, present JSON Schema whose properties were kind, format and
// review -- it validated the artifact RECORD, never the bytes. A
// name-resolution check passes that; the intent fails. So conformance must
// check WHAT the validator validates, and the two failures are distinct: a
// validator that does not exist, and one that resolves but describes
// something else.
type Validator struct {
	Type   string `json:"type"`
	Schema string `json:"schema"`
}

// Artifact is a produced file, referencing a declared kind rather than
// restating it.
type Artifact struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Root string `json:"root"`
	Path string `json:"path"`

	Bytes  int64  `json:"bytes"`
	Digest string `json:"digest"`

	// Presentation names a rendering primitive the HOST owns. An unknown value
	// is ignored rather than honoured.
	Presentation string `json:"presentation,omitempty"`
}
