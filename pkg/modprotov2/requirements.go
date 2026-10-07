package modprotov2

// Strength distinguishes a requirement that must hold from one that improves
// the result (§5).
//
// v1 could not say this at all: Requirement had no strength field, so a host
// could not tell a missing dependency that blocks an Operation from one that
// degrades it. For a notes module, d2 is preferred (diagrams render without
// it, worse); an API key is mandatory.
type Strength string

const (
	StrengthMandatory Strength = "mandatory"
	StrengthPreferred Strength = "preferred"
)

// Requirement is a semantic precondition, declared with its strength.
type Requirement struct {
	// Kind is "binary", "version", "config", "env", or "credential".
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Strength Strength `json:"strength"`
	Detail   string   `json:"detail,omitempty"`
}

// State is a Resolution outcome. THREE states, never a bool (§5).
//
// v1 had Requirement.Available bool -- two states for a three-state world. The
// missing one is UNKNOWN, and it is not a technicality: a credential that is
// present but never exercised is neither satisfied nor unsatisfied, and
// reporting it as either is a lie in a different direction. A rendering
// module has two providers in exactly this state.
//
// `disabled` is NOT a Resolution state. It is USER INTENT ("do not use this
// even though it works"), and all three states here are statements about
// whether the thing WORKS. A lane with a four-state model would flatten
// disabled into unsatisfied and report a working provider as broken; a module
// author proposed keeping enablement product-side rather than widening the
// contract, and that is the right call.
type State string

const (
	StateSatisfied   State = "satisfied"
	StateUnsatisfied State = "unsatisfied"
	StateUnknown     State = "unknown"
)

// Valid reports whether s is one of the three legal states.
//
// An unrecognised value is NOT quietly treated as unknown. "Unknown" is a
// deliberate claim a module makes about a probe it could not complete; an
// unparseable value is a module speaking a vocabulary this host does not have.
// Collapsing the second into the first would let a typo read as a considered
// answer.
func (s State) Valid() bool {
	return s == StateSatisfied || s == StateUnsatisfied || s == StateUnknown
}

// Resolution is a requirement as EVALUATED at runtime.
type Resolution struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	State  State  `json:"state"`
	Detail string `json:"detail,omitempty"`
}
