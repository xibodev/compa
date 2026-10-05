package modproto

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// Host-side validation of module output
// ---------------------------------------------------------------------------
//
// Module output is untrusted input. A module is a separate process, often from
// a separate repository, and under the cross-checked-fixture model it has its
// own independent implementation of this contract. So the host validates rather
// than assumes -- including the invariants this package's own constructors
// already guarantee, because a module is not obliged to use them.
//
// These checks are deliberately about PROTOCOL WELL-FORMEDNESS only. Whether a
// capability's result matches its declared schema is a separate, schema-driven
// concern; this is the layer that decides whether the bytes are a valid
// envelope at all.

// ValidationError describes one contract violation, naming the JSON path so a
// failure points at a field rather than at a document.
type ValidationError struct {
	Path   string
	Detail string
}

func (e ValidationError) Error() string { return e.Path + ": " + e.Detail }

// ValidationErrors is the full set of violations found in one document. All
// violations are collected rather than failing at the first, so a module author
// sees every problem in one pass.
type ValidationErrors []ValidationError

func (errs ValidationErrors) Error() string {
	if len(errs) == 0 {
		return "no validation errors"
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Error()
	}
	return fmt.Sprintf("%d contract violation(s): %s", len(errs), strings.Join(parts, "; "))
}

// ValidateEnvelope checks a decoded envelope against the protocol's invariants.
//
// expectRequestID is the ID the host generated for this invocation; the module
// must echo it verbatim. Pass "" for describe, which has no request.
func ValidateEnvelope(e *Envelope, expectOperation, expectRequestID string) error {
	var errs ValidationErrors
	bad := func(path, detail string) {
		errs = append(errs, ValidationError{Path: path, Detail: detail})
	}

	if e.Protocol != ProtocolID {
		bad("protocol", fmt.Sprintf("got %q, want %q", e.Protocol, ProtocolID))
	}
	if e.Module == "" {
		bad("module", "must not be empty")
	}
	// Operation mirrors the two verbs the host speaks, and nothing else. It is
	// deliberately NOT the capability ID: the capability already travels in
	// Request.Capability and is correlated by request_id, so putting it here
	// too would duplicate it and let "operation" mean different things
	// depending on which module answered.
	//
	// Both the expectation and the reported value are checked. Validating only
	// that they match would let a caller's typo define the contract, which is
	// how a shared field with no pinned VALUE becomes the place two lanes
	// silently diverge -- a module lane hit exactly this and its validator
	// passed both conventions.
	switch expectOperation {
	case OperationDescribe, OperationInvoke:
	default:
		bad("operation", fmt.Sprintf("host asked for %q, which is not a protocol verb; want %q or %q",
			expectOperation, OperationDescribe, OperationInvoke))
	}
	if e.Operation != expectOperation {
		bad("operation", fmt.Sprintf("got %q, want %q", e.Operation, expectOperation))
	}
	// On invoke, request_id is echoed verbatim so the host can correlate
	// without trusting the module to generate a unique value; a module that
	// invents its own would break correlation silently.
	//
	// On DESCRIBE there is no request and therefore no host-generated ID, so
	// there is nothing to echo and nothing to correlate. Whatever a module puts
	// there is its own business -- several use it for their internal logging.
	// Demanding an empty string would be a house rule enforced as a protocol
	// rule, which rejects conforming modules; a real module was rejected by
	// exactly that mistake.
	if expectRequestID != "" && e.RequestID != expectRequestID {
		bad("request_id", fmt.Sprintf("got %q, want the host-generated %q echoed verbatim", e.RequestID, expectRequestID))
	}

	// Exactly one of result/error, matching ok.
	switch {
	case e.OK && e.Error != nil:
		bad("error", "must be absent when ok is true")
	case e.OK && len(e.Result) == 0:
		bad("result", "must be present when ok is true")
	case !e.OK && e.Error == nil:
		bad("error", "must be present when ok is false")
	case !e.OK && len(e.Result) > 0:
		bad("result", "must be absent when ok is false")
	}

	if e.Error != nil {
		if e.Error.Code == "" {
			bad("error.code", "must be a stable, non-empty code; the host routes on it and never on message text")
		}
		if e.Error.Message == "" {
			bad("error.message", "must not be empty")
		}
		if e.Error.Details == nil {
			bad("error.details", "must be {} rather than null")
		}
	}

	if e.Warnings == nil {
		bad("warnings", "must be [] rather than null")
	}
	if e.Execution.Artifacts == nil {
		bad("execution.artifacts", "must be [] rather than null")
	}

	for i, a := range e.Execution.Artifacts {
		p := fmt.Sprintf("execution.artifacts[%d]", i)
		if a.ID == "" {
			bad(p+".id", "must not be empty")
		}
		if a.Root == "" {
			bad(p+".root", "must name a root the host supplied in the request")
		}
		if a.Path == "" {
			bad(p+".path", "must not be empty")
		}
		// Confinement is enforced separately against the real roots; this
		// catches the shape that makes confinement impossible to check.
		if isAbsolutePath(a.Path) {
			bad(p+".path", "must be relative to its declared root, not absolute")
		}
		if !ValidDigest(a.Digest) {
			bad(p+".digest", fmt.Sprintf("got %q, want %q followed by 64 lowercase hex characters", a.Digest, DigestPrefix))
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// ValidateDescriptor checks a decoded descriptor against the protocol's
// invariants, including that it speaks a protocol version the host supports.
func ValidateDescriptor(d *Descriptor) error {
	var errs ValidationErrors
	bad := func(path, detail string) {
		errs = append(errs, ValidationError{Path: path, Detail: detail})
	}

	if d.Module == "" {
		bad("module", "must not be empty")
	}
	if d.Name == "" {
		bad("name", "must not be empty")
	}
	if d.Version == "" {
		bad("version", "must not be empty")
	}

	switch {
	case d.ProtocolVersions == nil:
		bad("protocol_versions", "must be [] rather than null")
	case len(d.ProtocolVersions) == 0:
		bad("protocol_versions", "must list at least one protocol version")
	default:
		supported := false
		for _, v := range d.ProtocolVersions {
			if v == ProtocolID {
				supported = true
				break
			}
		}
		if !supported {
			bad("protocol_versions", fmt.Sprintf("no overlap with host support; module speaks %v, host speaks %q", d.ProtocolVersions, ProtocolID))
		}
	}

	if d.Capabilities == nil {
		bad("capabilities", "must be [] rather than null")
	}

	seen := make(map[string]bool, len(d.Capabilities))
	for i, c := range d.Capabilities {
		p := fmt.Sprintf("capabilities[%d]", i)
		if c.ID == "" {
			bad(p+".id", "must not be empty")
		}
		// The ID is passed on the module's command line. One a flag parser
		// would read as an option, or one that is not a single argument, could
		// not be invoked safely.
		if strings.HasPrefix(c.ID, "-") {
			bad(p+".id", fmt.Sprintf("%q must not start with '-'; it is passed on the command line", c.ID))
		}
		if strings.IndexFunc(c.ID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			bad(p+".id", fmt.Sprintf("%q must not contain whitespace or control characters", c.ID))
		}
		if seen[c.ID] {
			bad(p+".id", fmt.Sprintf("duplicate capability ID %q", c.ID))
		}
		seen[c.ID] = true

		if c.Summary == "" {
			// Summary is what the host folds into the agent's capability
			// list, so an empty one silently costs the agent its only
			// description of the capability.
			bad(p+".summary", "must not be empty; it is the one line the agent sees")
		}
		if c.ArtifactSchemas == nil {
			bad(p+".artifact_schemas", "must be [] rather than null")
		}
		if c.Skills == nil {
			bad(p+".skills", "must be [] rather than null")
		}

		// Schema references must resolve, or the host cannot validate calls.
		if c.RequestSchema != "" {
			if _, ok := d.RequestSchemas[c.RequestSchema]; !ok {
				bad(p+".request_schema", fmt.Sprintf("references %q, which is not in request_schemas", c.RequestSchema))
			}
		}
		if c.ResultSchema != "" {
			if _, ok := d.ResultSchemas[c.ResultSchema]; !ok {
				bad(p+".result_schema", fmt.Sprintf("references %q, which is not in result_schemas", c.ResultSchema))
			}
		}
		for _, id := range c.ArtifactSchemas {
			if _, ok := d.ArtifactSchemas[id]; !ok {
				bad(p+".artifact_schemas", fmt.Sprintf("references %q, which is not in artifact_schemas", id))
			}
		}

		// A long-running capability the host cannot poll is a capability the
		// host can start and never observe.
		if c.LongRunning && c.PollCapability == "" {
			bad(p+".poll_capability", "must be set when long_running is true, or the host cannot observe the job")
		}
		if !c.LongRunning && c.PollCapability != "" {
			bad(p+".poll_capability", "must be empty when long_running is false")
		}
	}

	// A schema map key must agree with the $id inside the document it maps to.
	//
	// Checking only that a reference resolves to a KEY is not enough: a
	// descriptor can key its schemas by one scheme while the documents declare
	// another, every reference then "resolves", and the host validates a
	// request against a schema describing something else entirely. The failure
	// is silent and produces confident wrong answers, which is worse than a
	// dangling reference that fails loudly. Raised by a module author, who
	// found the same gap in their own validator.
	//
	// $id is optional -- a schema without one is simply identified by its key.
	// Only a PRESENT and DISAGREEING $id is a violation.
	checkSchemaIDs := func(field string, schemas map[string]json.RawMessage) {
		for key, doc := range schemas {
			var probe struct {
				ID string `json:"$id"`
			}
			if err := json.Unmarshal(doc, &probe); err != nil {
				bad(fmt.Sprintf("%s[%q]", field, key), "is not a decodable JSON Schema document")
				continue
			}
			// A JSON Schema $id is the DOCUMENT's own identity, often in the
			// module's own namespace ("openmontage/artifacts/render_report").
			// The map key is how THIS descriptor references it. Those are two
			// naming systems, not two claims about one thing, so requiring
			// equality rejects a legitimate and common convention -- a real
			// rendering module was refused outright by that mistake.
			//
			// What actually matters is that a key cannot silently point at a
			// document describing something ELSE. A trailing-segment match
			// catches that while allowing namespacing: key "render_report"
			// accepts $id ".../render_report" and rejects $id ".../scene_plan".
			if probe.ID != "" && !schemaIDMatchesKey(probe.ID, key) {
				bad(fmt.Sprintf("%s[%q].$id", field, key), fmt.Sprintf(
					"document declares $id %q, whose final segment does not match the key; a reference would resolve to a schema describing something else", probe.ID))
			}
		}
	}
	checkSchemaIDs("request_schemas", d.RequestSchemas)
	checkSchemaIDs("result_schemas", d.ResultSchemas)
	checkSchemaIDs("artifact_schemas", d.ArtifactSchemas)

	// Poll targets are resolved after all IDs are known, so forward references
	// are legal.
	for i, c := range d.Capabilities {
		if c.PollCapability != "" && !seen[c.PollCapability] {
			bad(fmt.Sprintf("capabilities[%d].poll_capability", i),
				fmt.Sprintf("references %q, which is not a declared capability", c.PollCapability))
		}
	}

	for i, o := range d.AgentOverlays {
		p := fmt.Sprintf("agent_overlays[%d]", i)
		if o.ID == "" {
			bad(p+".id", "must not be empty")
		}
		if isAbsolutePath(o.Path) {
			bad(p+".path", "must be relative to the module root, not absolute")
		}
		if !ValidDigest(o.Digest) {
			bad(p+".digest", fmt.Sprintf("got %q, want %q followed by 64 lowercase hex characters", o.Digest, DigestPrefix))
		}
	}

	for i, s := range d.Skills {
		p := fmt.Sprintf("skills[%d]", i)
		if s.ID == "" {
			bad(p+".id", "must not be empty")
		}
		if isAbsolutePath(s.Path) {
			bad(p+".path", "must be relative to the module root, not absolute")
		}
		if !ValidDigest(s.Digest) {
			bad(p+".digest", fmt.Sprintf("got %q, want %q followed by 64 lowercase hex characters", s.Digest, DigestPrefix))
		}
	}

	if len(errs) > 0 {
		return errs
	}
	return nil
}

// schemaIDMatchesKey reports whether a document's $id plausibly identifies the
// same schema as the map key that references it.
//
// Namespacing is allowed: only the final path segment must agree, and a
// trailing version suffix on either side is ignored, since "render_report",
// "render_report/v1" and "openmontage/artifacts/render_report" all name one
// schema. What is rejected is a final segment naming a DIFFERENT schema, which
// is the failure worth catching -- a reference resolving to a document about
// something else, silently.
func schemaIDMatchesKey(id, key string) bool {
	norm := func(s string) string {
		s = strings.TrimSuffix(s, "/")
		if i := strings.LastIndex(s, "/"); i >= 0 {
			// Drop a trailing version segment ("…/render_report/v1") before
			// taking the final name.
			last := s[i+1:]
			if len(last) > 1 && last[0] == 'v' && last[1] >= '0' && last[1] <= '9' {
				s = s[:i]
			}
		}
		if i := strings.LastIndex(s, "/"); i >= 0 {
			s = s[i+1:]
		}
		return strings.ToLower(s)
	}
	return norm(id) == norm(key)
}

// isAbsolutePath reports whether p looks absolute on ANY supported platform.
//
// filepath.IsAbs is host-platform-specific, which is the wrong test here: a
// module may emit a Windows-style path that a Linux host must still reject, and
// vice versa. Rejecting both shapes everywhere keeps the check independent of
// where the host happens to run.
// IsAbsolutePath reports whether p looks absolute on ANY supported platform.
//
// Exported because three other call sites were using filepath.IsAbs, which is
// host-specific: on Linux it is literally HasPrefix("/"), so a module emitting
// "C:\Windows\System32\config\sam" would not be refused there. A path
// the host must reject does not depend on where the host happens to run.
//
// One implementation, so a fix cannot leave a copy behind -- the same reason
// WithinRoot was exported after three copies of a broken prefix check survived
// being fixed once.
func IsAbsolutePath(p string) bool { return isAbsolutePath(p) }

func isAbsolutePath(p string) bool {
	if p == "" {
		return false
	}
	if p[0] == '/' || p[0] == '\\' {
		return true
	}
	// Drive-letter form: C:\ or C:/
	if len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') {
		isLetter := (p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')
		if isLetter {
			return true
		}
	}
	return false
}

// DecodeEnvelope parses module stdout into an envelope, enforcing that stdout
// carried EXACTLY ONE JSON document and nothing else.
//
// This is the stdout-purity rule made executable. A module that prints a
// progress line, a warning, or a second document has violated the contract even
// when the first document parses, so trailing content is an error rather than
// something to ignore. That is also what makes stderr-only diagnostics
// enforceable rather than merely requested.
func DecodeEnvelope(stdout []byte) (*Envelope, error) {
	dec := json.NewDecoder(strings.NewReader(string(stdout)))

	var e Envelope
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("%s: stdout is not a single JSON envelope: %w", ErrHostInvalidJSON, err)
	}

	// Anything after the first document is a protocol violation, whether or not
	// it is itself valid JSON.
	//
	// Checking this by attempting a second Decode is subtly wrong and was a real
	// bug here: a trailing LOG LINE makes the second Decode fail, which reads as
	// "nothing follows" while being exactly the case worth catching. Trailing
	// garbage is far likelier than a trailing second document, so the test is
	// whether any non-whitespace byte remains.
	rest, err := io.ReadAll(dec.Buffered())
	if err != nil {
		return nil, fmt.Errorf("%s: could not inspect trailing stdout: %w", ErrHostProtocolViolation, err)
	}
	if trailing := strings.TrimSpace(string(rest)); trailing != "" {
		return nil, fmt.Errorf(
			"%s: stdout carried %d trailing byte(s) after the envelope (%q); exactly one JSON envelope is allowed and diagnostics belong on stderr",
			ErrHostProtocolViolation, len(trailing), truncateForError(trailing))
	}

	return &e, nil
}

// truncateForError bounds untrusted module output before it appears in an error
// message, so a misbehaving module cannot flood host logs or agent context with
// its own payload. The donor did exactly that: its "invalid JSON" path echoed
// the entire response back into the model's context.
func truncateForError(s string) string {
	const max = 120
	if len(s) <= max {
		return s
	}
	return s[:max] + "... (truncated)"
}

// ---------------------------------------------------------------------------
// Declared effects vs. reported execution
// ---------------------------------------------------------------------------

// ValidateExecutionAgainstDeclared checks what an invocation ACTUALLY did
// against what its capability DECLARED it would do.
//
// This is the check that protects real money and real files, and it is
// deliberately shaped to avoid a trap a module author named: the capability is
// looked up in the descriptor rather than accepted as a caller-supplied
// argument. A validator that takes the declaration as a parameter merely
// ratifies whatever the caller believed, so a wrong belief becomes a passing
// test -- the same shape as a validator that accepts any verb the caller
// invents.
//
// It reports violations in the SAFE direction only. A capability that declared
// network access and stayed local is fine; one that declared local and reached
// the network is not. Under-promising is allowed, over-reaching is not.
func ValidateExecutionAgainstDeclared(d *Descriptor, capabilityID string, e *Execution) error {
	_, err := CheckExecutionAgainstDeclared(d, capabilityID, e)
	return err
}

// CheckExecutionAgainstDeclared is ValidateExecutionAgainstDeclared with the
// advisory findings separated from the refusals, so a host can surface an
// effect over-reach without failing the call.
func CheckExecutionAgainstDeclared(d *Descriptor, capabilityID string, e *Execution) (warnings []ValidationError, err error) {
	var errs ValidationErrors
	bad := func(path, detail string) {
		errs = append(errs, ValidationError{Path: path, Detail: detail})
	}
	warn := func(path, detail string) {
		warnings = append(warnings, ValidationError{Path: path, Detail: detail})
	}

	var cap *Capability
	for i := range d.Capabilities {
		if d.Capabilities[i].ID == capabilityID {
			cap = &d.Capabilities[i]
			break
		}
	}
	if cap == nil {
		// Not a soft failure: an execution that cannot be checked against a
		// declaration is an execution with no approval basis at all.
		return nil, ValidationErrors{{
			Path:   "capability",
			Detail: fmt.Sprintf("%q is not declared by module %q, so its execution cannot be checked against any declared effect", capabilityID, d.Module),
		}}
	}

	// Effect over-reach is a WARNING-class finding in v1, not a hard refusal.
	//
	// Running the real modules showed the declared/reported comparison is not
	// yet unambiguous. A rendering module's `creative.tools.estimate` declares
	// itself local while reporting network=true and provider="google_flow" --
	// it is describing the RUN it priced, not the estimate call, which touched
	// no network. Both readings are defensible and the protocol has not said
	// which is meant, so refusing the invocation would block a correct module
	// over an unsettled question.
	//
	// The finding is still surfaced, because an undeclared write or network
	// reach is exactly what approval routing depends on. It is returned
	// separately from hard violations so the host can show it without failing
	// the call. Once the lanes settle whether `execution` may describe a priced
	// future call, this becomes a refusal again.
	if e.Network && !cap.Effects.Network {
		warn("execution.network", fmt.Sprintf("capability %q declared no network access but the invocation reported network use", capabilityID))
	}
	if e.ExternalWrites && !cap.Effects.ExternalWrites {
		warn("execution.external_writes", fmt.Sprintf("capability %q declared no external writes but the invocation reported writing outside the host's control", capabilityID))
	}

	// The provider a call reports is shown to the operator on the Modules page,
	// beside the cost, and it is how a person tells "this ran locally" from
	// "this reached a paid service". It was never compared to what the
	// capability declared -- so a capability declaring provider "local" could
	// report reaching one that bills, and the page would print it as fact.
	//
	// A warning rather than a refusal, matching the writes and network cases
	// directly above: the same unsettled question about whether `execution`
	// describes this call or a priced future one applies, and refusing would
	// block a correct module over it. The finding still surfaces, because
	// provider is exactly what approval routing reads.
	//
	// An empty reported provider is not a claim, and "local" is never a
	// contradiction -- a capability may legitimately run locally whatever it
	// declared it might reach.
	//
	// "varies" IS ALSO NOT A CLAIM, and omitting it was an asymmetry in this
	// rule rather than a gap in the contract.
	//
	// The defect this check exists to catch is UNDERSTATEMENT: declare
	// something harmless, reach something that bills. A declaration of
	// "varies" understates nothing -- it is a refusal to claim, made at
	// describe time when the tool is not yet chosen, and reporting "ffprobe"
	// against it is MORE specific than declared rather than less.
	//
	// The asymmetry: this rule already exempts an unclaimed REPORTED value
	// ("") and did not exempt an unclaimed DECLARED one. "varies" is the
	// declaration-side twin of "".
	//
	// Found by a sibling lane running a capability WITH EFFECTS through the
	// real launcher. Their creative.tools.run dispatches 35 tools across
	// twelve providers and the capability description is read BEFORE the
	// request selects which, so "varies" can never equal any reported value --
	// the comparison was guaranteed to warn on every non-local run,
	// permanently, against a correct module. Same shape as may_charge for a
	// many-to-one projection: "the capability's provider" names no single
	// thing when 35 are reachable.
	//
	// KEPT NARROW ON PURPOSE. Only this one token is exempt, and only on the
	// DECLARED side. Any other value still has to match, so a capability that
	// genuinely names one provider cannot silence the check by declaring
	// something vague -- it would have to declare this exact word, which is a
	// visible statement rather than an accident.
	if e.Provider != "" && e.Provider != "local" &&
		cap.Effects.Provider != "" && !strings.EqualFold(cap.Effects.Provider, ProviderVaries) &&
		!strings.EqualFold(e.Provider, cap.Effects.Provider) {
		warn("execution.provider", fmt.Sprintf(
			"capability %q declared provider %q but the invocation reported %q; the cockpit shows the reported one to the operator",
			capabilityID, cap.Effects.Provider, e.Provider))
	}

	// The expensive case, and the reason Effects.CostKnown exists.
	//
	// An unpriced call reporting an exact 0 is indistinguishable from a free
	// one by looking at the envelope alone -- 0 is a valid number -- so only
	// the declaration reveals it.
	//
	// The check is scoped to calls that actually reached a provider, and that
	// scoping is not incidental. Running a real rendering module showed why:
	// `creative.tools.estimate` declares cost_known=false because the RUN it
	// prices is unpriced, yet the estimate call itself is local, touches no
	// network, and is free by design -- so reporting 0 is honest, not a lie.
	// Execution describes what THIS call did; Effects.CostKnown describes the
	// operation the capability performs. Conflating them rejects a correct
	// module for being truthful about a free call.
	//
	// A local call therefore may report 0. One that reached a provider may not,
	// because that is where money is actually spent.
	if !cap.Effects.CostKnown && e.Network {
		if e.ActualCost != nil && *e.ActualCost == 0 {
			warn("execution.actual_cost", fmt.Sprintf(
				"capability %q declares cost_known=false and this call reported reaching a provider, yet reports actual cost 0; unknown cost should stay null", capabilityID))
		}
		if e.EstimatedCost != nil && *e.EstimatedCost == 0 {
			warn("execution.estimated_cost", fmt.Sprintf(
				"capability %q declares cost_known=false and this call reported reaching a provider, yet reports estimated cost 0; unknown cost should stay null", capabilityID))
		}
	}

	// An artifact whose kind the capability never declared cannot be validated
	// against a schema, so the host has no basis to render it.
	for i, a := range e.Artifacts {
		declared := false
		for _, k := range cap.ArtifactSchemas {
			if k == a.Kind {
				declared = true
				break
			}
		}
		if !declared {
			bad(fmt.Sprintf("execution.artifacts[%d].kind", i), fmt.Sprintf(
				"capability %q produced artifact kind %q, which it does not declare in artifact_schemas", capabilityID, a.Kind))
		}
	}

	if len(errs) > 0 {
		return warnings, errs
	}
	return warnings, nil
}
