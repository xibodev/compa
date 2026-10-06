package api

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/view"
)

// The bug this exists for: the host tells the cockpit an artefact is a
// "document" or a "diagram", and the cockpit renders a viewer for it. This
// endpoint then served those same bytes as application/octet-stream, so the
// card promised a viewer the browser could not use.
//
// Observed: a .mmd resolved to "diagram" and was served as
// application/octet-stream.
//
// The two lists have to agree. This asserts that every extension this endpoint
// recognises resolves to an INLINE primitive -- if the served type collapses to
// "download", the card and the bytes disagree.
func TestServedTypesResolveToInlinePrimitives(t *testing.T) {
	extensions := []string{
		".mp4", ".m4v", ".webm", ".mp3", ".wav",
		".png", ".jpg", ".jpeg", ".gif", ".svg",
		".pdf", ".json", ".jsonl", ".md", ".csv", ".txt", ".log",
		".docx", ".doc", ".rtf", ".epub", ".pptx", ".ppt",
		".mmd", ".dot", ".gv", ".tsv",
	}

	r := view.NewRegistry()
	for _, ext := range extensions {
		served := contentTypeFor("artefact" + ext)
		if served == "application/octet-stream" {
			t.Errorf("%s: endpoint serves octet-stream, but it is in the"+
				" recognised list -- either serve a real type or remove it", ext)
			continue
		}
		if p := r.Resolve(served, ""); p == view.Download {
			t.Errorf("%s: served as %q, which pkg/view renders as a bare"+
				" download -- the card and the bytes disagree", ext, served)
		}
	}
}

// An unknown extension stays an octet stream. A wrong guess that happens to be
// executable is the failure worth avoiding, and it is why .svg is the only
// markup type served with its real type.
func TestUnknownExtensionStaysOpaque(t *testing.T) {
	for _, ext := range []string{".exe", ".dll", ".sh", ".html", ".htm", ".js", ".xml", ""} {
		if got := contentTypeFor("file" + ext); got != "application/octet-stream" {
			t.Errorf("%s served as %q, want application/octet-stream", ext, got)
		}
	}
}

// Extensions that could execute in the cockpit's origin must never be served
// with a renderable type, whatever else changes here.
func TestNoScriptableTypeIsServed(t *testing.T) {
	for _, ext := range []string{".html", ".htm", ".js", ".mjs", ".xhtml"} {
		got := strings.ToLower(contentTypeFor("x" + ext))
		if strings.Contains(got, "html") || strings.Contains(got, "javascript") {
			t.Fatalf("%s served as %q: scriptable in the cockpit's own origin", ext, got)
		}
	}
}
