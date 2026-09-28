// Package view defines shared declarative product-view definitions and
// rendering primitives for Compa and its consumer products.
//
// Layer 1 of view architecture:
//   - Primitive: rendering shapes (video, audio, markdown, slides, etc.)
//   - Card: host presentation of one artifact with provenance
//   - ViewDefinition: product-owned declarative workbench screens
//     shared identically between standalone apps (F-APP, M-APP) and
//     Compa module views (F-MOD, M-MOD).
package view

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// Primitive is a rendering shape the host UI knows how to draw.
type Primitive string

const (
	Video     Primitive = "video"
	Audio     Primitive = "audio"
	Image     Primitive = "image"
	ImageGrid Primitive = "image_grid"
	Timeline  Primitive = "timeline"
	Markdown  Primitive = "markdown"
	Text      Primitive = "text"
	JSON      Primitive = "json"
	Document  Primitive = "document"
	Diagram   Primitive = "diagram"
	Slides    Primitive = "slides"
	Table     Primitive = "table"
	Download  Primitive = "download"
)

// ViewState identifies standard operational states for view rendering.
type ViewState string

const (
	StateLoading          ViewState = "loading"
	StateEmpty            ViewState = "empty"
	StateFailed           ViewState = "failed"
	StateAwaitingApproval ViewState = "awaiting_approval"
	StateCompleted        ViewState = "completed"
	StateUnsupported      ViewState = "unsupported"
)

// SectionDefinition is a node in the view composition tree.
type SectionDefinition struct {
	ID        string              `json:"id"`
	Title     string              `json:"title,omitempty"`
	Type      string              `json:"type"` // "tab", "row", "col", "viewer", "card"
	Primitive Primitive           `json:"primitive,omitempty"`
	Binding   string              `json:"binding,omitempty"` // json path into state or artifact
	EmptyText string              `json:"empty_text,omitempty"`
	Children  []SectionDefinition `json:"children,omitempty"`
}

// ActionDefinition defines an explicit user action mapped to a product tool.
type ActionDefinition struct {
	ID               string         `json:"id"`
	Label            string         `json:"label"`
	Tool             string         `json:"tool"`
	InputTemplate    map[string]any `json:"input_template,omitempty"`
	RequiresApproval bool           `json:"requires_approval,omitempty"`
	KeyboardShortcut string         `json:"keyboard_shortcut,omitempty"`
	DisabledWhen     string         `json:"disabled_when,omitempty"`
}

// ViewDefinition is the shared declarative specification of a product workbench.
type ViewDefinition struct {
	ID             string              `json:"id"`
	SchemaVersion  string              `json:"schema_version"`
	Title          string              `json:"title"`
	Module         string              `json:"module"`
	ArtifactSchema string              `json:"artifact_schema,omitempty"`
	Sections       []SectionDefinition `json:"sections"`
	Actions        []ActionDefinition  `json:"actions,omitempty"`
}

// Digest returns the deterministic sha256: prefix digest of the canonical JSON definition.
// F-APP/F-MOD and M-APP/M-MOD use this to verify matching view definitions.
func (v *ViewDefinition) Digest() string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}

// Registry manages primitive activation state per user.
type Registry struct {
	disabled map[Primitive]bool
}

// NewRegistry constructs a new Primitive Registry.
func NewRegistry() *Registry {
	return &Registry{disabled: make(map[Primitive]bool)}
}

// SetEnabled toggles a primitive on or off.
func (r *Registry) SetEnabled(p Primitive, enabled bool) {
	if enabled {
		delete(r.disabled, p)
		return
	}
	r.disabled[p] = true
}

// Enabled reports whether a primitive is active.
func (r *Registry) Enabled(p Primitive) bool {
	return !r.disabled[p]
}

// Resolve chooses how to render an artifact based on media type and hint.
func (r *Registry) Resolve(mediaType, presentationHint string) Primitive {
	if hint := Primitive(strings.TrimSpace(presentationHint)); hint != "" {
		if Known(hint) && r.Enabled(hint) {
			return hint
		}
	}
	p := FromMediaType(mediaType)
	if r.Enabled(p) {
		return p
	}
	return Download
}

// FromMediaType maps a MIME content type to a standard rendering primitive.
func FromMediaType(mediaType string) Primitive {
	mt := strings.ToLower(strings.TrimSpace(mediaType))
	if i := strings.Index(mt, ";"); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}

	switch {
	case strings.HasPrefix(mt, "video/"):
		return Video
	case strings.HasPrefix(mt, "audio/"):
		return Audio
	case mt == "image/svg+xml":
		return Diagram
	case strings.HasPrefix(mt, "image/"):
		return Image
	case mt == "application/json", strings.HasSuffix(mt, "+json"):
		return JSON
	case mt == "text/markdown", mt == "text/x-markdown":
		return Markdown
	case mt == "application/pdf", mt == "application/epub+zip",
		mt == "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		mt == "application/msword", mt == "application/rtf":
		return Document
	case mt == "text/vnd.graphviz", mt == "text/vnd.mermaid":
		return Diagram
	case mt == "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		mt == "application/vnd.ms-powerpoint":
		return Slides
	case mt == "text/csv", mt == "text/tab-separated-values":
		return Table
	case strings.HasPrefix(mt, "text/"):
		return Text
	case mt == "":
		return JSON
	default:
		return Download
	}
}

// Known reports whether p is a recognized primitive.
func Known(p Primitive) bool {
	switch p {
	case Video, Audio, Image, ImageGrid, Timeline, Markdown, Text, JSON,
		Document, Diagram, Slides, Table, Download:
		return true
	}
	return false
}

// All returns all known primitives.
func All() []Primitive {
	return []Primitive{
		Video, Audio, Image, ImageGrid, Timeline,
		Document, Diagram, Slides, Table,
		Markdown, Text, JSON, Download,
	}
}

// Describe returns a human description for the primitive.
func Describe(p Primitive) string {
	switch p {
	case Video:
		return "Play video with audio and a scrub control."
	case Audio:
		return "Play audio, for narration and music."
	case Image:
		return "Show a single image."
	case ImageGrid:
		return "Show several images together, for scanning sampled frames at once."
	case Timeline:
		return "Show labelled time ranges on an axis, read-only."
	case Markdown:
		return "Render prose: briefs, scripts, reviews."
	case Text:
		return "Show plain text."
	case Document:
		return "Show paginated documents such as PDF inline."
	case Diagram:
		return "Render vector diagrams and diagram sources."
	case Slides:
		return "Show a deck as navigable pages."
	case Table:
		return "Show row and column data as a grid."
	case JSON:
		return "Pretty-print structured data."
	case Download:
		return "Offer the file for download when it cannot be shown inline."
	}
	return ""
}

// Card is the host presentation of one artifact.
type Card struct {
	ArtifactID string          `json:"artifact_id"`
	Kind       string          `json:"kind"`
	Module     string          `json:"module"`
	Primitive  Primitive       `json:"primitive"`
	MediaType  string          `json:"media_type"`
	Bytes      int64           `json:"bytes"`
	Digest     string          `json:"digest"`
	Title      string          `json:"title,omitempty"`
	URL        string          `json:"url"`
	Inline     json.RawMessage `json:"inline,omitempty"`
}
