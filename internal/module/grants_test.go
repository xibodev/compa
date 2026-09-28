package module

import (
	"testing"

	"github.com/xibodev/compa/pkg/modproto"
)

func descriptorWith(p modproto.Permissions) *modproto.Descriptor {
	return &modproto.Descriptor{Module: "test.module", Permissions: p}
}

// A grant may never exceed what the module declared. This is the property that
// keeps the descriptor honest: a host cannot confer authority for something a
// module never asked for, so reading the descriptor tells you the ceiling.
func TestApplyGrantsNeverExceedsDeclared(t *testing.T) {
	d := descriptorWith(modproto.Permissions{
		PaidProviders: []string{"gflow"},
	})
	req := &modproto.Request{}

	ApplyGrants(d, req, GrantPolicy{
		PaidProviders: []string{"gflow", "openai", "kling"},
		Network:       []string{"api.example.com"},
	})

	if got := len(req.Grants.PaidProviders); got != 1 {
		t.Fatalf("paid providers = %d, want 1: %v", got, req.Grants.PaidProviders)
	}
	if req.Grants.PaidProviders[0] != "gflow" {
		t.Fatalf("granted %q, want gflow", req.Grants.PaidProviders[0])
	}
	// Never declared, so never granted -- even though policy allows it.
	if len(req.Grants.Network) != 0 {
		t.Fatalf("granted undeclared network: %v", req.Grants.Network)
	}
}

// The trusted-desktop policy authorizes everything DECLARED, and still nothing
// more. This is the path the cockpit actually uses.
func TestGrantAllAuthorizesDeclaredOnly(t *testing.T) {
	d := descriptorWith(modproto.Permissions{
		PaidProviders: []string{"gflow", "kling"},
		Network:       []string{"api.gflow.dev"},
		Publish:       false,
	})
	req := &modproto.Request{}

	ApplyGrants(d, req, GrantAll())

	if len(req.Grants.PaidProviders) != 2 {
		t.Fatalf("paid providers = %v, want both declared", req.Grants.PaidProviders)
	}
	if len(req.Grants.Network) != 1 {
		t.Fatalf("network = %v, want the one declared", req.Grants.Network)
	}
	// The module did not declare publish, so a permissive policy cannot add it.
	if req.Grants.Publish {
		t.Fatal("publish granted though the module never declared it")
	}
}

// The regression this file exists for: before ApplyGrants, every request went
// out with an empty Grants value, which a module correctly reads as "nothing
// authorized". A paid capability would be refused even after a human approved
// the spend, and the failure would look like the module's fault.
func TestDeclaredPaidProviderIsActuallyGranted(t *testing.T) {
	d := descriptorWith(modproto.Permissions{
		PaidProviders: []string{"gflow", "openai", "kling", "elevenlabs", "pexels"},
	})
	req := &modproto.Request{}

	ApplyGrants(d, req, GrantAll())

	if len(req.Grants.PaidProviders) == 0 {
		t.Fatal("no paid provider granted: a module reading this fails closed" +
			" and every approved paid call is refused")
	}
}

// Nothing declared means nothing granted, regardless of policy.
func TestUndeclaredStaysUngranted(t *testing.T) {
	req := &modproto.Request{}
	ApplyGrants(descriptorWith(modproto.Permissions{}), req, GrantAll())

	if len(req.Grants.PaidProviders) != 0 || len(req.Grants.Network) != 0 ||
		len(req.Grants.Credentials) != 0 || req.Grants.Publish {
		t.Fatalf("granted something to a module that declared nothing: %+v", req.Grants)
	}
}

// An explicit deny-all policy grants nothing even for declared names, so a host
// that wants to withhold authority can.
func TestGrantNothingWithholdsDeclared(t *testing.T) {
	d := descriptorWith(modproto.Permissions{
		PaidProviders: []string{"gflow"},
		Publish:       true,
	})
	req := &modproto.Request{}

	ApplyGrants(d, req, GrantNothing())

	if len(req.Grants.PaidProviders) != 0 {
		t.Fatalf("paid providers = %v, want none", req.Grants.PaidProviders)
	}
	if req.Grants.Publish {
		t.Fatal("publish granted under a deny-all policy")
	}
}

// A nil descriptor or request must not panic: discovery can hand back a module
// that failed to describe itself.
func TestApplyGrantsHandlesNil(t *testing.T) {
	ApplyGrants(nil, &modproto.Request{}, GrantAll())
	ApplyGrants(descriptorWith(modproto.Permissions{}), nil, GrantAll())
}
