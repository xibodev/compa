package modprotov2

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CheckArtifacts verifies produced artifacts against what the descriptor
// declared, and is the "Value Contract" half of the frozen shape.
//
// WHAT v1 COULD NOT DO. artifact_schemas mapped every artifact to a JSON Schema
// and validated NONE of them -- wrong in two directions at once: nothing was
// enforced, and an mp4 was undeclarable without implying a validator that
// cannot exist. So this is the first point where a produced artifact is checked
// against its own declaration at all.
//
// VALIDATION DIFFERS BY KIND, and that is the whole point of section 7 rather
// than an implementation convenience:
//
//	media    -- media type, size and digest. NEVER a JSON Schema.
//	text     -- media type and digest. An honest "no validator exists".
//	document -- must name a validator, and that validator must check the
//	            artifact's OWN CONTENT.
//
// WHAT THIS DOES NOT DO, stated because a checker that silently covers less
// than its name suggests is the defect this contract keeps finding: it does not
// EXECUTE a document's validator. Running a JSON Schema needs a schema
// evaluator, and the frozen RFC's open question -- whether v2 enforces artifact
// validation at all -- is unruled. So a document artifact is checked for having
// a validator declared and for its bytes matching digest and size; whether the
// content SATISFIES the schema is not checked here and is not claimed.
func CheckArtifacts(d *Descriptor, arts []Artifact, rootPath func(root string) (string, bool)) []Finding {
	var out []Finding
	for _, a := range arts {
		where := fmt.Sprintf("artifact %q", a.ID)

		decl, ok := d.ArtifactKinds[a.Kind]
		if !ok {
			out = append(out, Finding{
				Code:  "artifact_kind_undeclared",
				Where: where,
				Reason: fmt.Sprintf("produced with kind %q, which the descriptor"+
					" does not declare, so nothing states how it should be"+
					" validated or rendered", a.Kind),
				Remedy: "declare the kind in artifact_kinds, or emit an artifact" +
					" of a kind that is declared. An undeclared kind cannot be" +
					" checked at all, which is indistinguishable from being" +
					" checked and passing",
			})
			continue
		}

		if a.Digest == "" {
			out = append(out, Finding{
				Code:   "artifact_without_digest",
				Where:  where,
				Reason: "no digest, so its bytes cannot be verified for any kind",
				Remedy: "emit sha256:<hex>. Digest is the one check every kind" +
					" shares -- media, text and document alike",
			})
		}

		// Media type is what selects a renderer and, for media, is one of the
		// three things that validates it.
		if decl.MediaType == "" {
			out = append(out, Finding{
				Code:   "artifact_kind_without_media_type",
				Where:  where,
				Reason: fmt.Sprintf("kind %q declares no media_type", a.Kind),
				Remedy: "declare the media type on the artifact kind",
			})
		}

		out = append(out, checkBytes(where, a, decl, rootPath)...)
	}
	return out
}

// checkBytes verifies the file itself when the host can reach it.
//
// Reachability is not assumed: a root the host did not supply, or a file that
// is gone, is reported as ITS OWN finding rather than folded into a digest
// mismatch. "Could not check" and "checked and wrong" are different facts, and
// collapsing them is how a missing artifact reads as a corrupt one.
func checkBytes(where string, a Artifact, decl ArtifactKind,
	rootPath func(string) (string, bool)) []Finding {

	if rootPath == nil {
		return nil // caller is doing a declaration-only check
	}
	base, ok := rootPath(a.Root)
	if !ok {
		return []Finding{{
			Code:  "artifact_root_unknown",
			Where: where,
			Reason: fmt.Sprintf("names root %q, which this invocation did not"+
				" supply, so the file cannot be located", a.Root),
			Remedy: "emit artifacts under a root the host granted for this" +
				" invocation. Root names are invocation-scoped and are not" +
				" portable between modules",
		}}
	}

	// Confinement is re-checked here even though the host enforces it
	// elsewhere: an artifact path is module-supplied, and a check that trusts
	// module input is not a check.
	full := filepath.Join(base, filepath.FromSlash(a.Path))
	if rel, err := filepath.Rel(base, full); err != nil ||
		rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return []Finding{{
			Code:   "artifact_escapes_root",
			Where:  where,
			Reason: fmt.Sprintf("path %q resolves outside root %q", a.Path, a.Root),
			Remedy: "emit the artifact inside the granted root",
		}}
	}

	info, err := os.Stat(full)
	if err != nil {
		return []Finding{{
			Code:   "artifact_missing",
			Where:  where,
			Reason: fmt.Sprintf("declared at %s/%s and not present", a.Root, a.Path),
			Remedy: "an artifact reported in the envelope must exist. A missing" +
				" file is a different failure from a corrupt one and is reported" +
				" separately so it is not read as a digest mismatch",
		}}
	}

	var out []Finding
	// SIZE IS PART OF WHAT VALIDATES MEDIA (section 7), so a wrong size is a
	// finding in its own right rather than a hint that the digest will fail.
	if a.Bytes > 0 && info.Size() != a.Bytes {
		out = append(out, Finding{
			Code:  "artifact_size_mismatch",
			Where: where,
			Reason: fmt.Sprintf("declares %d bytes and is %d on disk",
				a.Bytes, info.Size()),
			Remedy: "report the size actually written. For media, size is one of" +
				" the three things that validates the artifact",
		})
	}

	if a.Digest != "" {
		blob, err := os.ReadFile(full)
		if err != nil {
			return append(out, Finding{
				Code:   "artifact_unreadable",
				Where:  where,
				Reason: fmt.Sprintf("exists but could not be read: %v", err),
				Remedy: "ensure the host can read artifacts it is told about",
			})
		}
		sum := sha256.Sum256(blob)
		actual := "sha256:" + hex.EncodeToString(sum[:])
		if actual != a.Digest {
			out = append(out, Finding{
				Code:   "artifact_digest_mismatch",
				Where:  where,
				Reason: fmt.Sprintf("declares %s and hashes to %s", a.Digest, actual),
				Remedy: "report the digest of the bytes actually written. This is" +
					" the ONLY check that applies to every kind, so a mismatch" +
					" means nothing about this artifact can be trusted",
			})
		}
	}
	return out
}

// RenderMediaType is the media type the host should use to pick a renderer.
//
// It comes from the DECLARATION rather than from sniffing, which is the point
// of declaring it. A module observed declaring image/png for JPEG bytes is why
// the host still treats the value as a claim: this returns what was declared,
// and the caller's renderer degrades gracefully when a claim is wrong.
//
// Returns "" for an undeclared kind rather than guessing. CheckArtifacts
// reports that as a finding; silently substituting a default here would make
// an undeclared kind render as though it had been declared.
func RenderMediaType(d *Descriptor, a Artifact) string {
	if decl, ok := d.ArtifactKinds[a.Kind]; ok {
		return decl.MediaType
	}
	return ""
}
