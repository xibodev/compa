package module

import (
	"github.com/xibodev/compa/pkg/modproto"
	"os"
	"strings"
)

// GrantPolicy is what the HOST is willing to authorize for an invocation.
//
// It is the host's half of the grant: a module declares what it may need, the
// policy says what the host will actually allow, and the grant is the
// intersection. Neither side alone decides -- a module cannot widen its own
// authority by declaring more, and the host cannot confer authority for
// something the module never declared and therefore cannot be expecting.
//
// The zero value authorizes nothing, which is the correct default for a host
// that has not made a decision yet.
type GrantPolicy struct {
	// Network, Credentials and PaidProviders are the names the host will
	// authorize when a module declares them.
	Network       []string
	Credentials   []string
	PaidProviders []string

	// Subprocess is the set of executable NAMES the host will authorize.
	//
	// It is separate from Request.Binaries, which carries the resolved absolute
	// PATHS. A module reads the grant to decide whether it MAY shell out, and
	// the path to know what to run -- so filling only Binaries left a module
	// with a path it was not authorized to use. One module's evidence.extract
	// refused with "no AI CLI was granted for this invocation" one line after
	// the host printed "binaries: 3/3 resolved"; both were true, about
	// different fields.
	Subprocess []string

	// Publish is separate because it is a single capability rather than a set,
	// and because publishing is the one effect a user almost always wants to
	// approve per act rather than per session.
	Publish bool
}

// GrantAll authorizes everything a module declared.
//
// This is for the trusted single-user desktop case this host was built for:
// the person running the cockpit installed the module deliberately, and the
// approval that matters happens at the call, in front of them, not in a policy
// table they never wrote. Consent and cost gates still apply -- a grant says
// "you may reach this provider", never "you may spend this money".
func GrantAll() GrantPolicy {
	return GrantPolicy{Publish: true, Subprocess: subprocessOverride()}
}

// EnvSubprocessAllow names the executables the host may authorize, overriding
// the default of "everything the module declared".
//
// It exists because a module choosing among several declared binaries picks the
// first the host GRANTS, and the host cannot tell a signed-in CLI from a merely
// installed one -- an AI CLI's `--version` exits 0 on a machine where every real
// request fails to authenticate. Narrowing the grant is the one lever the host
// legitimately has: it is a statement about what this machine authorizes, not a
// guess about which CLI works.
//
// Unset means unchanged: every declared name is authorized, as before. Setting
// it is the user saying which of their CLIs they are actually signed in to.
const EnvSubprocessAllow = "COMPA_SUBPROCESS_ALLOW"

// subprocessOverride reads the allow-list, or nil when the user has not set one.
//
// nil is the important case: intersect() treats a nil policy list under
// allowAll as "authorize everything declared", so an unset variable changes
// nothing. An empty or whitespace-only value is also nil rather than "authorize
// nothing" -- a stray export should not silently disable every module's
// subprocess access with no error anywhere.
func subprocessOverride() []string {
	raw := strings.TrimSpace(os.Getenv(EnvSubprocessAllow))
	if raw == "" {
		return nil
	}
	var out []string
	for _, name := range strings.Split(raw, ",") {
		if n := strings.TrimSpace(name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// GrantNothing authorizes nothing while still being an explicit decision.
func GrantNothing() GrantPolicy {
	return GrantPolicy{}
}

// ApplyGrants fills req.Grants from what the module DECLARED, intersected with
// what the host policy authorizes.
//
// This is the per-invocation half of "installing a module grants nothing".
// Authority is conferred here, on this call, and only for names the module
// declared up front -- the same rule GrantBinaries applies to executables.
//
// Two properties are load-bearing:
//
//   - A grant is not consent. Authorizing a paid provider says the host will
//     let the module reach it; whether to spend money on this particular call
//     is a separate human decision the module still has to ask for. Collapsing
//     the two is how an unpriced call gets made because someone once ticked a
//     box.
//   - An empty grant list means "nothing authorized", and that is different
//     from a module being invoked outside any host mediation. A module that
//     receives an explicit empty list must fail closed rather than assume it
//     was called directly.
//
// Subprocess IS set here, and the comment that used to say otherwise was wrong
// in a way that cost a journey. The reasoning -- that req.Binaries carries the
// resolved paths, so naming them again would be a second source of truth --
// sounded right and left Grants.Subprocess permanently empty. A module reads
// the GRANT to decide whether it may shell out and the PATH to know what to
// run; supplying only the path handed it an executable with no permission to
// use it, and a real module refused with "no AI CLI was granted" one line
// after the host printed "binaries: 3/3 resolved".
//
// The two are not duplicates: one is authority, the other is location.
func ApplyGrants(d *modproto.Descriptor, req *modproto.Request, policy GrantPolicy) {
	if d == nil || req == nil {
		return
	}

	p := d.Permissions
	req.Grants = modproto.Grants{
		Network:       intersect(p.Network, policy.Network, policy.Publish),
		Credentials:   intersect(p.Credentials, policy.Credentials, policy.Publish),
		PaidProviders: intersect(p.PaidProviders, policy.PaidProviders, policy.Publish),
		// allowAll is deliberately FALSE when the policy names subprocesses:
		// an explicit allow-list is the operator narrowing what this machine
		// authorizes, and the trusted-host shortcut would discard it. The
		// result is still a subset of what the module declared, so the
		// override can only narrow, never widen.
		Subprocess: intersect(p.Subprocess, policy.Subprocess,
			policy.Publish && len(policy.Subprocess) == 0),
		Publish: p.Publish && policy.Publish,
	}
}

// intersect returns the declared names the policy authorizes.
//
// allowAll is the trusted-host shortcut: every declared name is authorized
// without the host having to enumerate names it has never seen. It still
// cannot confer authority for something undeclared, because the result is
// always a subset of `declared`.
//
// The result is non-nil whenever anything is authorized, and nil when nothing
// is -- a JSON null and an empty array both read as "nothing authorized", so
// the distinction that matters is grant-present versus grant-absent, which is
// decided by the Request carrying a Grants value at all.
func intersect(declared, allowed []string, allowAll bool) []string {
	if len(declared) == 0 {
		return nil
	}
	if allowAll {
		out := make([]string, len(declared))
		copy(out, declared)
		return out
	}

	permitted := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		permitted[name] = true
	}

	var out []string
	for _, name := range declared {
		if permitted[name] {
			out = append(out, name)
		}
	}
	return out
}
