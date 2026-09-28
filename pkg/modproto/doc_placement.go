package modproto

// This package lives under pkg/ rather than internal/ deliberately.
//
// internal/ is the right default for host code: it keeps the host's guts
// unimportable and lets them change freely. But this package is not host guts.
// It is the WIRE CONTRACT, and a contract with exactly one implementation is
// not a contract -- it is one program's opinion about a format.
//
// Under internal/, Go forbids the import across module boundaries, so every
// module lane had to hand-roll its own copy of the same types, digest helpers,
// and normalization rules. That produced two independent implementations of one
// contract within a day, which is precisely the drift the fixtures exist to
// catch, and it would have been caught late and quietly rather than at compile
// time.
//
// Placing it under pkg/ does NOT weaken the architecture's central rule.
// Modules remain detached local processes that the host executes and never
// links: no host package imports a module, and no module is imported into the
// host binary. What a module may now share is the DESCRIPTION OF THE WIRE
// FORMAT -- structs, tags, constants, and pure helpers -- with no host runtime,
// no policy, no execution, and no I/O.
//
// The distinction worth preserving: sharing a spec is not coupling; sharing a
// runtime is. This package must therefore stay dependency-free beyond the
// standard library, and must never grow discovery, policy, process execution,
// or anything else that would make importing it mean importing the host.
