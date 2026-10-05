// Package contractv2 declares the identity of the frozen xibodev.module/v2
// behavioural Target Contract and the exact-pin check that gates it.
//
// SCOPE. This package answers exactly one question: may the host rely on v2
// behavioural guarantees when talking to this counterparty? It deliberately
// contains no other v2 semantics. Contract vocabulary and facility
// implementation are separate concerns (operator ruling), so nothing here
// implements approval, budgets, resume, or effects.
//
// WHERE IT RUNS. modprotov2.Evaluate calls CheckPin on every described
// module's descriptor, and discovery (internal/moduletools) refuses a module
// whose pin is refused or whose v2 declaration does not conform: it is not
// registered. A module declaring no contract_version is served under v1.
//
// WHY A SEPARATE PACKAGE FROM modproto. xibodev.module/v1 is immutable legacy
// behaviour: nothing may be added to it, and tolerant decoding is not
// permission to smuggle successor semantics into a v1 exchange. modproto's
// ProtocolID versions the WIRE FORMAT. This versions BEHAVIOUR, which v1 never
// versioned at all -- and conflating the two is what let two sibling lanes
// infer behavioural guarantees from what a wire validator happened to accept.
//
// PINNING, NOT NEGOTIATION. By operator ruling, v2 uses an exact pinned
// identity. Module and host each declare exactly one behavioural contract
// version; equal means proceed to conformance evaluation, anything else means
// refuse deterministically with a reason and a remedy.
//
// Explicitly excluded, and this is the load-bearing part: version ranges,
// highest-common-version selection, downgrade, fallback, and pseudo-
// negotiation. A single-element list that merely LOOKS negotiable is excluded
// too. That is not a stylistic preference -- v1 shipped exactly that shape, a
// doc comment promising the host "selects the highest it also supports" over a
// membership test against one frozen constant, and a sibling lane built a
// versioning story on the promise before discovering it was inert. A structure
// that can only ever hold one value should not be shaped like a choice.
//
// Real negotiation may be designed later, only when multiple behavioural
// contract versions actually coexist, a host intentionally supports more than
// one, and a real consumer needs runtime selection. Until then exact pinning is
// simpler and more truthful.
package contractv2

import (
	"fmt"

	"github.com/xibodev/compa/pkg/modproto"
)

// ContractID is the behavioural contract this host implements.
//
// Deliberately a single string and not a slice. A []string would invite
// "declare several and pick one", which is the shape the ruling forbids, and
// the type is the cheapest place to make that impossible.
const ContractID = "xibodev.module/v2"

// Outcome is what the host may do with a counterparty, and there are THREE
// answers rather than two.
//
// An earlier version of this package returned a single OK boolean, which made
// "serve this module under v1" and "refuse this module" indistinguishable to a
// caller. Both are "not v2", and collapsing them would have refused every v1
// module the moment this gate was wired -- breaking the frozen contract's own
// compatibility rule (§10: v1 modules keep working; absent v2 fields fall back
// to v1 behaviour, never to a more permissive default).
//
// The sibling lane found this from the other side: they permit an absent
// contract_version for exactly this reason and refuse present-and-wrong. Same
// conclusion, reached independently, and their message is what prompted the
// re-read that found the conflation here.
type Outcome int

const (
	// OutcomeRefuse means the counterparty declared a behavioural contract this
	// host does not implement. It is a disagreement about what the guarantees
	// MEAN, so proceeding would apply v2 semantics to something that never
	// agreed to them.
	OutcomeRefuse Outcome = iota

	// OutcomeV1 means no behavioural contract was declared. This is a v1
	// module: it is served, with v1 behaviour, and NO v2 guarantee may be
	// relied upon. Not an error and not a refusal.
	OutcomeV1

	// OutcomeV2 means the pin matched exactly. Only this outcome permits
	// relying on a v2 behavioural guarantee.
	OutcomeV2
)

func (o Outcome) String() string {
	switch o {
	case OutcomeV2:
		return "v2"
	case OutcomeV1:
		return "v1"
	default:
		return "refuse"
	}
}

// PinResult reports the outcome of the contract pin check.
//
// Reason and Remedy are separate because they answer different questions and a
// non-conforming counterparty needs both: what is wrong, and what to do. A
// refusal that says only "incompatible" sends someone to read source.
type PinResult struct {
	Outcome Outcome
	Reason  string
	Remedy  string
}

// MayRelyOnV2 reports whether v2 behavioural guarantees are available.
//
// This is the ONLY question the conformance suite may ask before checking a v2
// guarantee. It is deliberately not named OK: "ok" invites reading a served v1
// module as success in the v2 sense, which is the conflation this type exists
// to prevent.
func (r PinResult) MayRelyOnV2() bool { return r.Outcome == OutcomeV2 }

// Served reports whether the host may talk to this module at all.
//
// True for both v2 and v1. False only for a declared contract this host does
// not implement.
func (r PinResult) Served() bool { return r.Outcome != OutcomeRefuse }

// Error renders a refusal for a caller that wants one, and nil when the module
// is served -- under v2 OR under v1. A v1 module is not an error condition.
func (r PinResult) Error() error {
	if r.Served() {
		return nil
	}
	return fmt.Errorf("%s. %s", r.Reason, r.Remedy)
}

// CheckPin compares a counterparty's declared behavioural contract version
// against this host's, and is the ONLY gate through which v2 guarantees become
// available.
//
// The empty string yields OutcomeV1, not a refusal. A module that declares
// nothing is a v1 module -- refusing it would break every module shipped today
// and violate the frozen contract's compatibility rule. It is still barred from
// every v2 guarantee, which is the part that matters: absent falls back to v1
// behaviour, never to a more permissive default.
//
// An EXPLICIT "xibodev.module/v1" is refused rather than served, and the
// distinction is deliberate. That string is the WIRE-FORMAT identity
// (modproto.ProtocolID); it is not a behavioural contract, because v1 behaviour
// was never versioned -- which is the entire premise of having a successor.
// Serving it would put a name in the conformance record that names nothing: a
// consumer reading "governed by xibodev.module/v1" could not tell it from a
// real contract identity, and §10 forbids falling back to a more permissive
// default. A contract name that names no guarantees IS the more permissive
// default. The sibling lane reached the same conclusion independently and both
// pins produce the same four outcomes.
//
// Nothing here inspects capabilities, effects or artifacts. Callers MUST pass
// this gate before relying on any v2 behavioural guarantee; that ordering is
// what the falsification test in the conformance suite pins.
func CheckPin(declared string) PinResult {
	switch declared {
	case ContractID:
		return PinResult{Outcome: OutcomeV2}
	case "":
		return PinResult{
			Outcome: OutcomeV1,
			Reason: fmt.Sprintf(
				"the module declares no behavioural contract version, so it is"+
					" served as xibodev.module/v1 and no %s guarantee may be"+
					" relied upon", ContractID),
			Remedy: fmt.Sprintf(
				"nothing to fix: v1 modules keep working. Declare"+
					" contract_version %q only when the module implements the"+
					" v2 behavioural guarantees", ContractID),
		}
	case modproto.ProtocolID:
		// Its own remedy, because this author made a DIFFERENT mistake from
		// someone declaring an unknown version, and the generic advice is
		// actively wrong for them.
		//
		// Telling a v1 caller to "declare contract_version v2" instructs them
		// to claim guarantees they do not implement -- the host would then
		// rely on v2 semantics against a module that never agreed to them,
		// which is precisely what this gate exists to prevent. The author is
		// right about their own intent; they are a v1 caller. What they got
		// wrong is which axis the field names.
		return PinResult{
			Outcome: OutcomeRefuse,
			Reason: fmt.Sprintf(
				"%q is the WIRE-FORMAT identity, not a behavioural contract:"+
					" v1 behaviour was never versioned, so declaring it as"+
					" contract_version names a contract with no guarantees and"+
					" no conformance suite", modproto.ProtocolID),
			Remedy: fmt.Sprintf(
				"omit contract_version entirely. A v1 module sends no"+
					" contract_version and is served under v1 with its wire"+
					" protocol unchanged. Declare %q only when the module"+
					" actually implements the v2 behavioural guarantees",
				ContractID),
		}
	default:
		return PinResult{
			Outcome: OutcomeRefuse,
			Reason: fmt.Sprintf(
				"behavioural contract mismatch: the module declares %q and this"+
					" host implements %q", declared, ContractID),
			Remedy: fmt.Sprintf(
				"declare contract_version %q. This host pins one exact version:"+
					" it does not negotiate, select a highest common version,"+
					" downgrade, or fall back", ContractID),
		}
	}
}
