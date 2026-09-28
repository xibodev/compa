package module_test

import (
	"testing"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/modproto"
)

// The bug this exists for: modproto.Grants has a Subprocess field, and NOTHING
// ever filled it. ApplyGrants built Network, Credentials, PaidProviders and
// Publish, and silently omitted the fifth.
//
// A module that reads grants.subprocess -- which is what the protocol tells it
// to do, since a grant is the authority and Binaries is only the resolved path
// -- saw an empty list and refused to run. One module's evidence.extract failed
// with "no AI CLI was granted for this invocation" while the host reported
// "binaries: 3/3 resolved" one line earlier. Both were telling the truth about
// different fields.
//
// That is journey A: mine sessions, then produce content from them. It stopped
// at the mining step.
func TestADeclaredSubprocessIsActuallyGranted(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	d.Permissions.Subprocess = []string{"alpha-cli", "beta-cli"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantAll())

	if len(req.Grants.Subprocess) == 0 {
		t.Fatal("a module declared subprocess binaries and the host granted" +
			" none, so a capability needing one refuses to run")
	}
	got := map[string]bool{}
	for _, s := range req.Grants.Subprocess {
		got[s] = true
	}
	for _, want := range []string{"alpha-cli", "beta-cli"} {
		if !got[want] {
			t.Errorf("declared %q was not granted", want)
		}
	}
}

// A module that declares no subprocess gets none: the grant is a subset of what
// was declared, never a superset.
func TestAnUndeclaredSubprocessIsNotGranted(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantAll())

	if len(req.Grants.Subprocess) != 0 {
		t.Fatalf("granted %v to a module that declared none", req.Grants.Subprocess)
	}
}

// GrantNothing must withhold subprocess authority too, or "authorize nothing"
// is not what it says.
func TestGrantNothingWithholdsSubprocess(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	d.Permissions.Subprocess = []string{"alpha-cli"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantNothing())

	if len(req.Grants.Subprocess) != 0 {
		t.Fatalf("GrantNothing granted %v", req.Grants.Subprocess)
	}
}

// The policy can narrow the set: a host that authorizes only one CLI must not
// hand over the others a module happens to declare.
func TestThePolicyCanNarrowTheSubprocessSet(t *testing.T) {
	d := &modproto.Descriptor{Module: "test.module"}
	d.Permissions.Subprocess = []string{"alpha-cli", "beta-cli", "gamma-cli"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantPolicy{Subprocess: []string{"alpha-cli"}})

	if len(req.Grants.Subprocess) != 1 || req.Grants.Subprocess[0] != "alpha-cli" {
		t.Fatalf("granted %v, want only alpha-cli", req.Grants.Subprocess)
	}
}

// The host cannot tell a signed-in CLI from an installed one: an AI CLI's
// `--version` exits 0 on a machine where every real request fails to
// authenticate. A module picking among several declared binaries takes the
// first the host GRANTS, so narrowing the grant is the one lever the host
// legitimately has -- a statement about what this machine authorizes, not a
// guess about which CLI works.
func TestTheOperatorCanNarrowWhichBinariesAreGranted(t *testing.T) {
	t.Setenv(module.EnvSubprocessAllow, "alpha-cli")

	d := &modproto.Descriptor{Module: "test.module"}
	d.Permissions.Subprocess = []string{"beta-cli", "alpha-cli", "gamma-cli"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantAll())

	if len(req.Grants.Subprocess) != 1 || req.Grants.Subprocess[0] != "alpha-cli" {
		t.Fatalf("granted %v, want only alpha-cli", req.Grants.Subprocess)
	}
}

// Unset means UNCHANGED. A host that quietly stopped granting subprocess
// authority because a variable was absent would break every module that needs
// one, with nothing to read.
func TestAnUnsetOverrideChangesNothing(t *testing.T) {
	t.Setenv(module.EnvSubprocessAllow, "")

	d := &modproto.Descriptor{Module: "test.module"}
	d.Permissions.Subprocess = []string{"beta-cli", "alpha-cli"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantAll())

	if len(req.Grants.Subprocess) != 2 {
		t.Fatalf("an unset override changed the grant to %v", req.Grants.Subprocess)
	}
}

// Whitespace is not a decision either. A stray export must not silently
// disable every module's subprocess access.
func TestAWhitespaceOverrideIsNotAnEmptyAllowList(t *testing.T) {
	t.Setenv(module.EnvSubprocessAllow, "   ,  , ")

	d := &modproto.Descriptor{Module: "test.module"}
	d.Permissions.Subprocess = []string{"alpha-cli"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantAll())

	if len(req.Grants.Subprocess) != 1 {
		t.Fatalf("whitespace disabled the grant: %v", req.Grants.Subprocess)
	}
}

// The override NARROWS, never widens: naming a binary the module never
// declared must not grant it.
func TestTheOverrideCannotGrantSomethingUndeclared(t *testing.T) {
	t.Setenv(module.EnvSubprocessAllow, "alpha-cli,rm")

	d := &modproto.Descriptor{Module: "test.module"}
	d.Permissions.Subprocess = []string{"alpha-cli"}

	req := &modproto.Request{}
	module.ApplyGrants(d, req, module.GrantAll())

	for _, got := range req.Grants.Subprocess {
		if got == "rm" {
			t.Fatal("the override granted a binary the module never declared")
		}
	}
	if len(req.Grants.Subprocess) != 1 {
		t.Fatalf("granted %v, want only the declared alpha-cli", req.Grants.Subprocess)
	}
}
