package modprotov2

import (
	"encoding/json"
	"testing"
)

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
