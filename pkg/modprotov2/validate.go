package modprotov2

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Validate checks a v2 descriptor against every frozen clause that is
// mechanically checkable, and returns findings rather than a single error so a
// module author sees ALL of them at once.
//
// SCOPE, stated because a validator that silently covers less than its name
// suggests is the defect this contract keeps finding. This checks declarations
// against each other. It does NOT open artifact files, run validators, or
// probe requirements -- those need a real invocation and live in the
// conformance suite that runs against a binary.
func Validate(d *Descriptor) []Finding {
	var out []Finding

	out = append(out, validateShape(d)...)
	out = append(out, validateArtifactKinds(d)...)
	out = append(out, validateArtifactKindReferences(d)...)
	out = append(out, validateOperations(d)...)
	out = append(out, validateCapabilities(d)...)
	out = append(out, CheckNoWeakening(d)...)
	return out
}

// validateShape reports a descriptor that declares v2 and then declares no v2
// content.
//
// REPORTED, NOT REFUSED, and the distinction took checking rather than
// instinct. A capability projecting NO Operation is explicitly legal -- a
// registry read transforms no product material -- so a module whose every
// capability is a registry read legitimately has zero Operations. Refusing that
// outright would reject a shape the frozen contract permits, which is a
// contract change wearing an implementation's clothes.
//
// What IS worth saying is that two very different states currently look
// identical: a module with genuinely no Operations, and a module whose v2
// payload is not built yet. A sibling lane reported exactly this against their
// own binary -- they publish contract_version with no operations because their
// projection work is held -- and correctly said the call was mine, not theirs.
//
// So the host says which one it is looking at and lets a person decide.
//
// AND IT SAYS PLAINLY THAT IT CANNOT DECIDE, which took a second correction to
// get right. My first version of this split claimed the host could classify the
// sibling's build from the descriptor alone. It cannot: the half-published
// discriminator fires only when artifact_kinds are PRESENT, and a lane that has
// not started migrating has neither Operations nor kinds. So the discriminator
// is unreachable for exactly the mid-migration population it was built to
// classify -- measured against their real binary, which publishes 21 v1
// artifact_schemas and zero v2 artifact_kinds.
//
// Nothing else on the wire decides it either: a capability declaring
// `projects: []` because it dispatches would differ from one declaring it as a
// registry read, but a pre-migration module declares `projects` at all. The
// case is genuinely undecidable until a module starts migrating, and a finding
// that says so is worth more than a signal invented to make it decidable.
func validateShape(d *Descriptor) []Finding {
	if len(d.Operations) > 0 || len(d.Capabilities) == 0 {
		return nil
	}

	// HALF-PUBLISHED IS UNAMBIGUOUS, and separating it out is the sharper
	// finding. artifact_kinds exist to be named by an Operation's `produces`;
	// with zero Operations NOTHING can produce them. So a descriptor declaring
	// artifact kinds and no Operations is not the ambiguous case at all -- it is
	// a partially-built v2 payload, and the host can say so rather than offering
	// a reading that cannot be true.
	//
	// The sibling lane's own guard test asserts exactly this shape
	// (hasOperations == hasArtifactKinds), written for section 10's
	// half-populated concern before either of us connected it to this finding.
	if len(d.ArtifactKinds) > 0 {
		return []Finding{{
			Code:  "v2_payload_half_published",
			Where: fmt.Sprintf("module %q", d.Module),
			Reason: fmt.Sprintf("declares %d artifact kinds and NO Operations."+
				" Artifact kinds are named by an Operation's `produces`, so with"+
				" no Operations nothing can produce them -- this is a partially"+
				" built v2 payload, not a module that genuinely has no Operations",
				len(d.ArtifactKinds)),
			Remedy: "declare the Operations that produce these artifact kinds. Until" +
				" then the no-weakening check has nothing to compare against and" +
				" passes WITHOUT CHECKING ANYTHING, which is indistinguishable from" +
				" passing because the projection is sound",
		}}
	}
	for _, c := range d.Capabilities {
		if len(c.Projects) > 0 {
			// A capability projects something that does not exist.
			// CheckNoWeakening reports that precisely; do not duplicate it.
			return nil
		}
	}
	return []Finding{{
		Code:  "no_operations_declared",
		Where: fmt.Sprintf("module %q", d.Module),
		Reason: fmt.Sprintf("declares a v2 contract with %d capabilities and NO"+
			" Operations, so nothing states what this module MEANS semantically"+
			" -- only what it exposes. THIS HOST CANNOT TELL WHICH CASE IT IS:"+
			" the descriptor carries no v2 field that distinguishes them",
			len(d.Capabilities)),
		Remedy: "if every capability really is a registry read that transforms no" +
			" product material, this is correct and expected -- no action. If the" +
			" Operation layer is simply not built yet, the v2 declaration is" +
			" AHEAD OF THE PAYLOAD: the no-weakening check has nothing to compare" +
			" against and passes WITHOUT CHECKING ANYTHING, which is" +
			" indistinguishable from passing because the projection is sound." +
			" A person who knows the module must decide; the wire cannot",
	}}
}

// validateArtifactKinds enforces §7's three kinds and what each may say.
func validateArtifactKinds(d *Descriptor) []Finding {
	var out []Finding
	for name, ak := range d.ArtifactKinds {
		where := fmt.Sprintf("artifact_kind %q", name)
		switch ak.Kind {
		case KindDocument:
			// FAILURE (a) FROM SECTION 9: a named validator that DOES NOT EXIST.
			//
			// Checked against the descriptor's own schema maps, because a schema
			// id that resolves nowhere is a promise of a check that cannot run.
			// This is DISTINCT from naming no validator at all: one author forgot
			// to declare a validator, the other declared one and got the id
			// wrong, and they need different remedies.
			if ak.Validator != nil && ak.Validator.Schema != "" {
				_, inResult := d.ResultSchemas[ak.Validator.Schema]
				_, inRequest := d.RequestSchemas[ak.Validator.Schema]
				schema, found := d.ResultSchemas[ak.Validator.Schema]
				if !found {
					schema = d.RequestSchemas[ak.Validator.Schema]
				}
				if hits, total := looksLikeARecordSchema(schema); len(hits) > 0 {
					out = append(out, Finding{
						Code:  "validator_may_describe_the_record",
						Where: where,
						Reason: fmt.Sprintf("validator schema %q has %d of %d top-level"+
							" properties drawn from the artifact-RECORD vocabulary (%v),"+
							" so it may validate a description OF the artifact rather"+
							" than its content. THIS IS A HEURISTIC, not a proof: a"+
							" content schema may legitimately use these names",
							ak.Validator.Schema, len(hits), total, hits),
						Remedy: "confirm the validator describes the artifact's OWN" +
							" CONTENT. If it describes the record, declare the kind" +
							" `text` instead -- an honest 'no validator exists' is" +
							" stronger than a validator pointed at the wrong object," +
							" which reads as a checked artifact and is not one",
					})
				}
				if !inResult && !inRequest {
					out = append(out, Finding{
						Code:  "validator_schema_missing",
						Where: where,
						Reason: fmt.Sprintf("names validator schema %q, which this"+
							" descriptor does not declare, so the validator cannot"+
							" be resolved and the check it promises can never run",
							ak.Validator.Schema),
						Remedy: "declare the schema in result_schemas or" +
							" request_schemas, or declare the kind `text` if no" +
							" validator exists. A named-but-absent validator is worse" +
							" than none: it reads as a checked artifact",
					})
				}
			}
			if ak.Validator == nil || ak.Validator.Schema == "" {
				out = append(out, Finding{
					Code:   "document_without_validator",
					Where:  where,
					Reason: "declared as `document` but names no validator",
					Remedy: "name a validator that validates the ARTIFACT'S OWN" +
						" CONTENT, or declare it `text`. `text` is an honest" +
						" statement that no validator exists and is not a lesser" +
						" declaration",
				})
			}
		case KindMedia:
			// Naming a validator here is an ERROR rather than an omission: a
			// JSON Schema cannot validate an mp4, so the declaration would
			// promise a check nothing can perform.
			if ak.Validator != nil {
				out = append(out, Finding{
					Code:  "media_with_validator",
					Where: where,
					Reason: "declared as `media` but names a validator; media is" +
						" validated by media type, size and digest, never by JSON Schema",
					Remedy: "remove the validator. If the content really is a" +
						" structured document, declare it `document` instead",
				})
			}
			if ak.MediaType == "" {
				out = append(out, Finding{
					Code:   "media_without_media_type",
					Where:  where,
					Reason: "declared as `media` with no media_type, so nothing can be checked about it",
					Remedy: "declare the media type; it is one of the three things that validates media",
				})
			}
		case KindText:
			if ak.MediaType == "" {
				out = append(out, Finding{
					Code:   "text_without_media_type",
					Where:  where,
					Reason: "declared as `text` with no media_type",
					Remedy: "declare the media type; `text` means media type plus digest and nothing more",
				})
			}
		default:
			out = append(out, Finding{
				Code:   "unknown_artifact_kind",
				Where:  where,
				Reason: fmt.Sprintf("kind %q is not one of document, text, media", ak.Kind),
				Remedy: "use one of the three. An unrecognised kind is refused" +
					" rather than treated as text, because a typo must not read" +
					" as a considered claim that no validator exists",
			})
		}
	}
	return out
}

// validateArtifactKindReferences reports an artifact kind that NO Operation
// produces.
//
// THE REVERSE DIRECTION, and it was missing. validateOperations already checks
// `produces` -> artifact_kinds (naming a kind that does not exist). Nothing
// checked artifact_kinds -> `produces`, so a declared kind that nothing points
// at sat there looking correct indefinitely.
//
// That is the UNRESOLVED REFERENCE class a sibling lane named this week after
// finding two of them in one code path: "a value stays correct-looking
// indefinitely when no consumer resolves it." A duplicate at least has a second
// copy to disagree with; an unresolved reference has nothing to disagree with
// at all.
//
// A WARNING RATHER THAN A HARD ERROR in spirit -- callers weigh findings by
// code -- because a module may legitimately declare a kind it produces only on
// a path not yet modelled. But it must be VISIBLE: silence here is what let the
// class survive elsewhere.
func validateArtifactKindReferences(d *Descriptor) []Finding {
	if len(d.ArtifactKinds) == 0 || len(d.Operations) == 0 {
		// With no Operations at all, validateShape already reports the bigger
		// fact. Reporting every kind as unreferenced on top of it would bury
		// the finding that matters under N copies of its consequence.
		return nil
	}

	produced := map[string]bool{}
	for _, op := range d.Operations {
		for _, k := range op.Produces {
			produced[k] = true
		}
	}

	var names []string
	for name := range d.ArtifactKinds {
		if !produced[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names) // deterministic output; map order is not

	var out []Finding
	for _, name := range names {
		out = append(out, Finding{
			Code:  "artifact_kind_unreferenced",
			Where: fmt.Sprintf("artifact_kind %q", name),
			Reason: "no Operation declares that it produces this kind, so nothing" +
				" in the descriptor resolves the reference and it cannot be" +
				" reached through any projection",
			Remedy: "name it in the `produces` of the Operation that emits it, or" +
				" remove it. An unreferenced declaration stays correct-looking" +
				" indefinitely because no consumer ever resolves it",
		})
	}
	return out
}

// looksLikeARecordSchema reports whether a validator schema appears to describe
// the artifact RECORD rather than the artifact's CONTENT.
//
// FAILURE (b) FROM SECTION 9, and the honest implementation of it. The frozen
// text requires conformance to check WHAT THE VALIDATOR VALIDATES, with the
// concrete case being a real module's: application/json declared `document`,
// naming a real, present JSON Schema whose properties are kind, format and
// review -- it validates a description OF the artifact, never its bytes. A
// name-resolution check passes that; the intent fails.
//
// THIS IS A HEURISTIC AND IS REPORTED AS ONE. A schema whose top-level
// properties are drawn from the artifact-record vocabulary is probably
// describing the record -- but a content schema could legitimately have a field
// called "path", so this cannot be a proof. Reporting a suspicion AS A
// CERTAINTY would be the same defect as passing a check that never ran, one
// direction over: a finding that claims more than it measured.
//
// So the finding says what it observed and why, and leaves the judgement to a
// person. What it must NOT do is stay silent, because silence here is exactly
// how the superseded "names a validator that resolves" bar survived.
func looksLikeARecordSchema(raw []byte) (hits []string, total int) {
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Properties) == 0 {
		return nil, 0
	}
	// The v2 Artifact struct's own field names ARE the record vocabulary.
	recordFields := map[string]bool{
		"id": true, "kind": true, "root": true, "path": true,
		"bytes": true, "digest": true, "presentation": true,
		"format": true, "media_type": true,
	}
	for name := range doc.Properties {
		if recordFields[strings.ToLower(name)] {
			hits = append(hits, name)
		}
	}
	sort.Strings(hits)
	return hits, len(doc.Properties)
}

func validateOperations(d *Descriptor) []Finding {
	var out []Finding
	for _, op := range d.Operations {
		where := fmt.Sprintf("operation %q", op.ID)
		out = append(out, validateEffects(where, op.Effects)...)

		for _, r := range op.Requirements {
			if r.Strength != StrengthMandatory && r.Strength != StrengthPreferred {
				out = append(out, Finding{
					Code:   "requirement_without_strength",
					Where:  fmt.Sprintf("%s requirement %q", where, r.Name),
					Reason: fmt.Sprintf("strength is %q, not mandatory or preferred", r.Strength),
					Remedy: "declare mandatory or preferred. Without it a host" +
						" cannot tell a dependency that BLOCKS this Operation" +
						" from one that merely degrades it, which is the gap v1" +
						" had and this field closes",
				})
			}
		}

		for _, kind := range op.Produces {
			if _, ok := d.ArtifactKinds[kind]; !ok {
				out = append(out, Finding{
					Code:   "undeclared_artifact_kind",
					Where:  where,
					Reason: fmt.Sprintf("produces %q, which artifact_kinds does not declare", kind),
					Remedy: "declare the kind, or stop naming it. An artifact whose" +
						" kind is undeclared cannot be validated or rendered",
				})
			}
		}

		for _, reason := range op.Approval.RequiredWhen {
			switch reason {
			case "chargeable", "irreversible", "external_write", "product_checkpoint":
			default:
				out = append(out, Finding{
					Code:   "unknown_approval_reason",
					Where:  where,
					Reason: fmt.Sprintf("approval reason %q is not in the legal set", reason),
					Remedy: "use chargeable, irreversible, external_write or" +
						" product_checkpoint. The reason is what lets a host tell" +
						" a free-but-irreversible step from a paid one",
				})
			}
		}
	}
	return out
}

func validateCapabilities(d *Descriptor) []Finding {
	var out []Finding
	for _, c := range d.Capabilities {
		// A capability projecting NO Operation is LEGAL and is not reported.
		// A registry read transforms no product material; it is still
		// invocable, so it still declares effects, which is why the effects
		// check runs regardless of len(Projects).
		out = append(out, validateEffects(fmt.Sprintf("capability %q", c.ID), c.Effects)...)
	}
	return out
}

// validateEffects enforces §4's determinism rule, which is a claim falsified by
// its own siblings rather than by an external probe.
func validateEffects(where string, e Effects) []Finding {
	var out []Finding
	if !e.Deterministic {
		return out
	}
	if e.Network {
		out = append(out, Finding{
			Code:   "determinism_contradicted",
			Where:  where,
			Reason: "declares deterministic:true and network:true; a network call cannot promise the same bytes",
			Remedy: "set deterministic:false. Determinism is what makes" +
				" re-execution free recovery, so claiming it over a network call" +
				" offers a retry that may return something else",
		})
	}
	// A charging Operation cannot be deterministic: the second run costs money
	// the first already spent, so re-execution is not free recovery even when
	// the bytes match.
	if e.MayCharge.IsConditional() || e.MayCharge.Always {
		out = append(out, Finding{
			Code:  "determinism_contradicted",
			Where: where,
			Reason: "declares deterministic:true while may_charge can be true;" +
				" re-execution is not free recovery when it spends again",
			Remedy: "set deterministic:false, or declare may_charge:false if it" +
				" genuinely never charges",
		})
	}
	return out
}
