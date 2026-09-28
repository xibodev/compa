package modprotov2

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Artifacts are checked against REAL FILES with REAL DIGESTS.
//
// A fixture where the test author controls both the declaration and the bytes
// cannot tell you the check works -- it tells you the author was consistent.
// These write actual files, hash them with the same algorithm the host uses,
// and then corrupt them.
func writeArtifact(t *testing.T, dir, name, content string) (int64, string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(content))
	return int64(len(content)), "sha256:" + hex.EncodeToString(sum[:])
}

func mediaDescriptor() *Descriptor {
	return &Descriptor{
		Module: "render",
		ArtifactKinds: map[string]ArtifactKind{
			"render_mp4":   {Kind: KindMedia, MediaType: "video/mp4"},
			"captions_srt": {Kind: KindText, MediaType: "text/plain"},
			"render_report": {Kind: KindDocument, MediaType: "application/json",
				Validator: &Validator{Type: "json_schema", Schema: "x/v1"}},
		},
	}
}

// An honest artifact passes, so every failure below fails for the mutation and
// not for an unrelated setup error.
func TestAnHonestArtifactPasses(t *testing.T) {
	dir := t.TempDir()
	size, digest := writeArtifact(t, dir, "out.mp4", "fake mp4 bytes")

	f := CheckArtifacts(mediaDescriptor(),
		[]Artifact{{ID: "out", Kind: "render_mp4", Root: "workspace",
			Path: "out.mp4", Bytes: size, Digest: digest}},
		func(string) (string, bool) { return dir, true })

	if len(f) != 0 {
		t.Fatalf("an honest artifact produced findings: %s", codes(f))
	}
}

// MEDIA IS VALIDATED BY MEDIA TYPE, SIZE AND DIGEST -- never JSON Schema.
//
// Each of the three is checked separately, because they fail for different
// reasons and a caller that only ever sees "digest mismatch" cannot tell a
// truncated write from a corrupted one.
func TestMediaIsValidatedByDigestAndSizeSeparately(t *testing.T) {
	t.Run("corrupt bytes fail the digest", func(t *testing.T) {
		dir := t.TempDir()
		size, digest := writeArtifact(t, dir, "out.mp4", "original")
		// Same LENGTH, different bytes: only the digest can catch this.
		if err := os.WriteFile(filepath.Join(dir, "out.mp4"), []byte("modified"), 0o644); err != nil {
			t.Fatal(err)
		}

		f := CheckArtifacts(mediaDescriptor(),
			[]Artifact{{ID: "out", Kind: "render_mp4", Root: "w", Path: "out.mp4",
				Bytes: size, Digest: digest}},
			func(string) (string, bool) { return dir, true })

		if !containsCode(f, "artifact_digest_mismatch") {
			t.Fatalf("bytes changed with the size preserved and the digest check"+
				" did not fire: %s", codes(f))
		}
		if containsCode(f, "artifact_size_mismatch") {
			t.Error("reported a size mismatch when the size is identical")
		}
	})

	t.Run("wrong size is its own finding", func(t *testing.T) {
		dir := t.TempDir()
		_, digest := writeArtifact(t, dir, "out.mp4", "twelve bytes")

		f := CheckArtifacts(mediaDescriptor(),
			[]Artifact{{ID: "out", Kind: "render_mp4", Root: "w", Path: "out.mp4",
				Bytes: 9999, Digest: digest}},
			func(string) (string, bool) { return dir, true })

		if !containsCode(f, "artifact_size_mismatch") {
			t.Fatalf("a wrong declared size was not reported: %s", codes(f))
		}
		// The digest is correct, so it must NOT also report a mismatch --
		// otherwise one defect produces two findings and an author fixes the
		// wrong thing first.
		if containsCode(f, "artifact_digest_mismatch") {
			t.Error("reported a digest mismatch for bytes that hash correctly")
		}
	})
}

// A MISSING artifact is not a corrupt one.
//
// Collapsing them is how a module that never wrote the file reads as one that
// wrote it wrong -- and the remedies are completely different.
func TestAMissingArtifactIsDistinguishedFromACorruptOne(t *testing.T) {
	dir := t.TempDir()

	missing := CheckArtifacts(mediaDescriptor(),
		[]Artifact{{ID: "out", Kind: "render_mp4", Root: "w", Path: "never.mp4",
			Bytes: 10, Digest: "sha256:abc"}},
		func(string) (string, bool) { return dir, true })

	if !containsCode(missing, "artifact_missing") {
		t.Fatalf("an absent file was not reported as missing: %s", codes(missing))
	}
	if containsCode(missing, "artifact_digest_mismatch") {
		t.Error("an absent file was reported as a digest mismatch, which sends" +
			" the author to check bytes that do not exist")
	}
}

// An UNREACHABLE root is reported as its own fact.
//
// "Could not check" and "checked and wrong" are different, and a host that
// reports the first as the second turns a missing grant into an accusation
// against the module.
func TestAnUnreachableRootIsReportedAsSuchNotAsCorruption(t *testing.T) {
	f := CheckArtifacts(mediaDescriptor(),
		[]Artifact{{ID: "out", Kind: "render_mp4", Root: "not_granted",
			Path: "out.mp4", Digest: "sha256:abc"}},
		func(string) (string, bool) { return "", false })

	if !containsCode(f, "artifact_root_unknown") {
		t.Fatalf("an ungranted root was not reported: %s", codes(f))
	}
	if containsCode(f, "artifact_missing") || containsCode(f, "artifact_digest_mismatch") {
		t.Error("an ungranted root produced a file-level finding, so a missing" +
			" grant reads as a module defect")
	}
}

// A path escaping its root is refused before the file is touched.
//
// The host enforces confinement elsewhere, and this checks it again because an
// artifact path is MODULE-SUPPLIED. A check that trusts module input is not a
// check.
func TestAPathEscapingItsRootIsRefused(t *testing.T) {
	dir := t.TempDir()

	f := CheckArtifacts(mediaDescriptor(),
		[]Artifact{{ID: "out", Kind: "render_mp4", Root: "w",
			Path: "../../etc/passwd", Digest: "sha256:abc"}},
		func(string) (string, bool) { return dir, true })

	if !containsCode(f, "artifact_escapes_root") {
		t.Fatalf("a path escaping its root was not refused: %s", codes(f))
	}
}

// An artifact of an UNDECLARED kind is reported rather than checked loosely.
//
// Nothing states how it should be validated or rendered, so "checked and
// passed" would be a claim about a check that never happened.
func TestAnUndeclaredKindIsReported(t *testing.T) {
	dir := t.TempDir()
	size, digest := writeArtifact(t, dir, "x.bin", "bytes")

	f := CheckArtifacts(mediaDescriptor(),
		[]Artifact{{ID: "out", Kind: "mystery_format", Root: "w", Path: "x.bin",
			Bytes: size, Digest: digest}},
		func(string) (string, bool) { return dir, true })

	if !containsCode(f, "artifact_kind_undeclared") {
		t.Fatalf("an undeclared kind was accepted: %s", codes(f))
	}
}

// An artifact with NO digest is reported for every kind.
//
// Digest is the one check media, text and document all share, so its absence
// means nothing about the artifact can be verified.
func TestAnArtifactWithoutADigestIsReportedForEveryKind(t *testing.T) {
	dir := t.TempDir()
	for _, kind := range []string{"render_mp4", "captions_srt", "render_report"} {
		size, _ := writeArtifact(t, dir, "f", "content")
		f := CheckArtifacts(mediaDescriptor(),
			[]Artifact{{ID: "out", Kind: kind, Root: "w", Path: "f", Bytes: size}},
			func(string) (string, bool) { return dir, true })

		if !containsCode(f, "artifact_without_digest") {
			t.Errorf("kind %q: a digestless artifact was accepted: %s", kind, codes(f))
		}
	}
}

// The renderer's media type comes from the DECLARATION, and an undeclared kind
// yields "" rather than a guess.
//
// Substituting a default would make an undeclared kind render as though it had
// been declared -- the same shape as a check that passes because it did not run.
func TestRenderMediaTypeComesFromTheDeclarationAndNeverGuesses(t *testing.T) {
	d := mediaDescriptor()

	if got := RenderMediaType(d, Artifact{Kind: "render_mp4"}); got != "video/mp4" {
		t.Errorf("declared kind resolved to %q, want video/mp4", got)
	}
	if got := RenderMediaType(d, Artifact{Kind: "unknown"}); got != "" {
		t.Errorf("an UNDECLARED kind resolved to %q. Returning a default here"+
			" would render it as though it had been declared", got)
	}
}

// Declaration-only checking is possible without touching the filesystem.
//
// The host runs this at describe time, before any invocation has produced
// anything, so it must not require a root resolver.
func TestArtifactsCanBeCheckedWithoutFilesystemAccess(t *testing.T) {
	f := CheckArtifacts(mediaDescriptor(),
		[]Artifact{{ID: "out", Kind: "render_mp4", Root: "w", Path: "out.mp4",
			Digest: "sha256:abc"}}, nil)

	if len(f) != 0 {
		t.Fatalf("a declaration-only check produced file findings: %s", codes(f))
	}
}

// SECTION 9 REQUIRES TWO DISTINCT FAILURES, and I had implemented NEITHER.
//
// The frozen text: "a named validator that does not exist, and one that
// resolves but describes something else... Conformance MUST check what the
// validator validates."
//
// I shipped document_without_validator -- which fires when NO validator is
// NAMED -- and described the gap as "unruled". It is not unruled: §9 settles it
// as MUST. What IS unruled is whether v2 enforces artifact validation at
// RUNTIME, which is a different question, and I conflated the two.
//
// Found because a sibling lane reviewed my SCOPE DISCLOSURE and said they could
// not corroborate it, being structurally unable to test it -- zero document
// artifacts in their tree. That prompted the re-read.
func TestSection9sTwoValidatorFailuresAreBothChecked(t *testing.T) {
	t.Run("(a) a named validator that does not exist", func(t *testing.T) {
		d := &Descriptor{
			Module: "m",
			ArtifactKinds: map[string]ArtifactKind{
				"report": {Kind: KindDocument, MediaType: "application/json",
					Validator: &Validator{Type: "json_schema", Schema: "absent/v1"}},
			},
			ResultSchemas: map[string]json.RawMessage{},
		}
		f := Validate(d)
		if !containsCode(f, "validator_schema_missing") {
			t.Fatalf("a validator naming a schema the descriptor does not"+
				" declare was accepted: %s", codes(f))
		}
		// It must NOT be reported as "no validator named" -- different author,
		// different mistake, different fix.
		if containsCode(f, "document_without_validator") {
			t.Error("a validator with a WRONG id was reported as a MISSING" +
				" validator. One author forgot to declare one; the other" +
				" declared one and got the id wrong")
		}
	})

	t.Run("(b) resolves but describes the RECORD", func(t *testing.T) {
		// A real module's case, which is the one §9 cites: a present JSON Schema
		// whose properties are kind/format/review -- it validates a description
		// OF the artifact, never its bytes.
		d := &Descriptor{
			Module: "archive",
			ArtifactKinds: map[string]ArtifactKind{
				"render_report": {Kind: KindDocument, MediaType: "application/json",
					Validator: &Validator{Type: "json_schema", Schema: "rec/v1"}},
			},
			ResultSchemas: map[string]json.RawMessage{
				"rec/v1": json.RawMessage(`{"type":"object","properties":{
				  "kind":{"type":"string"},"format":{"type":"string"},
				  "review":{"type":"string"}}}`),
			},
		}
		f := Validate(d)
		if !containsCode(f, "validator_may_describe_the_record") {
			t.Fatalf("a validator describing the artifact RECORD passed. A"+
				" name-resolution check accepts this and the intent fails,"+
				" which is precisely what §9 says is not enough: %s", codes(f))
		}
		// The finding must ADMIT it is a heuristic. Reporting a suspicion as a
		// certainty is the same defect one direction over.
		for _, x := range f {
			if x.Code == "validator_may_describe_the_record" {
				if !contains(x.Reason, "HEURISTIC") {
					t.Errorf("the finding does not admit it is a heuristic: %q", x.Reason)
				}
			}
		}
	})

	t.Run("a genuine CONTENT schema is not flagged", func(t *testing.T) {
		d := &Descriptor{
			Module: "m",
			ArtifactKinds: map[string]ArtifactKind{
				"report": {Kind: KindDocument, MediaType: "application/json",
					Validator: &Validator{Type: "json_schema", Schema: "content/v1"}},
			},
			ResultSchemas: map[string]json.RawMessage{
				"content/v1": json.RawMessage(`{"type":"object","properties":{
				  "frames":{"type":"array"},"duration_ms":{"type":"integer"},
				  "codec":{"type":"string"}}}`),
			},
		}
		if f := Validate(d); len(f) != 0 {
			t.Fatalf("a validator describing real artifact CONTENT was flagged:"+
				" %s. The heuristic must not fire on every document, or it"+
				" trains authors to ignore it", codes(f))
		}
	})
}

// The record heuristic matches EXACT names, never substrings.
//
// LOAD-BEARING AND PREVIOUSLY UNASSERTED. A sibling lane attacked the heuristic
// against their 20 real authoring schemas -- because `len(hits) > 0` over a
// nine-name vocabulary looks reckless -- and found zero false positives. The
// reason is the exact-name match: their genuine content fields CONTAIN record
// words (`output_path`, `preview_path`, `project_id`) and substring matching
// would have tripped all three.
//
// So the exact match is what makes the threshold usable, and nothing tested it.
// A finding that fires on legitimate work is not a weaker check -- IT IS A CHECK
// THAT GETS DISABLED.
func TestTheRecordHeuristicMatchesExactNamesNotSubstrings(t *testing.T) {
	contentSchemaWithRecordishNames := &Descriptor{
		Module: "render",
		ArtifactKinds: map[string]ArtifactKind{
			"report": {Kind: KindDocument, MediaType: "application/json",
				Validator: &Validator{Type: "json_schema", Schema: "c/v1"}},
		},
		ResultSchemas: map[string]json.RawMessage{
			// Every one of these CONTAINS a record word and is not one.
			"c/v1": json.RawMessage(`{"type":"object","properties":{
			  "output_path":{"type":"string"},
			  "preview_path":{"type":"string"},
			  "project_id":{"type":"string"},
			  "kind_of_shot":{"type":"string"},
			  "digest_algorithm":{"type":"string"}}}`),
		},
	}

	if f := Validate(contentSchemaWithRecordishNames); len(f) != 0 {
		t.Fatalf("content fields CONTAINING record words were flagged: %s."+
			" Substring matching would trip output_path, preview_path and"+
			" project_id -- all legitimate content fields in a sibling's real"+
			" schemas", codes(f))
	}

	// And the exact names still fire, or the check above passes for the wrong
	// reason (a heuristic that never fires also has zero false positives).
	exact := &Descriptor{
		Module: "archive",
		ArtifactKinds: map[string]ArtifactKind{
			"report": {Kind: KindDocument, MediaType: "application/json",
				Validator: &Validator{Type: "json_schema", Schema: "r/v1"}},
		},
		ResultSchemas: map[string]json.RawMessage{
			"r/v1": json.RawMessage(`{"type":"object","properties":{
			  "kind":{"type":"string"},"format":{"type":"string"}}}`),
		},
	}
	if !containsCode(Validate(exact), "validator_may_describe_the_record") {
		t.Fatal("exact record names did not fire, so the negative case above" +
			" proves nothing: a heuristic that never fires has zero false" +
			" positives and zero value")
	}
}
