package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v2/pkg/approval"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/modproto"
)

func approvalModule() *modproto.Descriptor {
	return &modproto.Descriptor{
		Module: "approval.fixture",
		Capabilities: []modproto.Capability{
			{ID: "free", Effects: modproto.Effects{Local: true, CostKnown: true}},
			{ID: "writes", Effects: modproto.Effects{Local: true, ExternalWrites: true, CostKnown: true}},
			{ID: "unpriced", Effects: modproto.Effects{Network: true}},
		},
	}
}

// module-invoke decides a run by the approval policy with origin cli, as the
// agent's calls and the Modules page's runs are decided: an agent running the
// CLI through exec must not get past what the policy asks about or denies.
func TestModuleInvokeFollowsTheApprovalPolicy(t *testing.T) {
	d := approvalModule()
	def := approval.DefaultPolicy()
	rule := func(r approval.Rule) approval.Policy {
		return approval.Policy{Default: approval.Allow, Rules: []approval.Rule{r}}
	}
	for _, tc := range []struct {
		name     string
		policy   approval.Policy
		cap      string
		approve  bool
		approved bool
		refusal  string // empty when the run goes ahead
	}{
		{"the default allows", def, "free", false, false, ""},
		{"--approve does not approve what the default allows", def, "free", true, false, ""},
		{"the default asks about writes", def, "writes", false, false, "--approve"},
		{"an ask answered with --approve", def, "writes", true, true, ""},
		{"the default asks about an unknown cost", def, "unpriced", false, false, "--approve"},
		{"a rule allows", rule(approval.Rule{Source: "module:approval.fixture", Action: approval.Allow}), "unpriced", false, true, ""},
		{"a rule for cli asks", rule(approval.Rule{Tool: "approval_fixture__free", Origin: []approval.Origin{approval.OriginCLI}, Action: approval.Ask}), "free", false, false, "--approve"},
		{"a rule for web only", rule(approval.Rule{Tool: "approval_fixture__free", Origin: []approval.Origin{approval.OriginWeb}, Action: approval.Deny}), "free", false, false, ""},
		{"deny", rule(approval.Rule{Hints: []string{approval.HintNetwork}, Action: approval.Deny}), "unpriced", true, false, "denied by the approval policy"},
		{"hide", rule(approval.Rule{Source: "module:*", Action: approval.Hide}), "free", true, false, "denied by the approval policy"},
		// An undeclared capability is refused even when approved: its effects are unknown.
		{"undeclared", def, "missing", true, false, "declares no capability"},
	} {
		approved, err := invokeApproval(tc.policy, d, tc.cap, tc.approve)
		if approved != tc.approved || (err != nil) != (tc.refusal != "") {
			t.Errorf("%s: approved %v, err %v; want approved %v, refused %v",
				tc.name, approved, err, tc.approved, tc.refusal != "")
		}
		if err != nil && !strings.Contains(err.Error(), tc.refusal) {
			t.Errorf("%s: the refusal does not say %q: %v", tc.name, tc.refusal, err)
		}
	}
}

func TestModuleInvokeHasApproveFlag(t *testing.T) {
	flag := NewModuleInvokeCommand().Flags().Lookup("approve")
	if flag == nil {
		t.Fatal("module-invoke has no --approve flag")
	}
	if flag.Value.Type() != "bool" || flag.DefValue != "false" {
		t.Fatalf("--approve is %s defaulting to %s, want a bool defaulting to false", flag.Value.Type(), flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "agent") {
		t.Errorf("--approve help does not warn that an agent can pass it too: %s", flag.Usage)
	}
}

// End to end against the fake module: a capability the policy asks about does
// not start without --approve and runs with it, one a rule allows runs without
// it, one the policy denies does not run either way, and a config that cannot
// be read refuses the run.
func TestModuleInvokeAppliesTheConfiguredPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	t.Setenv(config.EnvConfig, cfgPath)
	installCLIEnabledFakeModule(t, home)
	setPolicy := func(policy string) {
		t.Helper()
		if err := os.WriteFile(cfgPath, []byte(`{"tools":{"approval":`+policy+`}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const unpriced = "fake.estimate.unpriced"

	res, err := invokeModule("fake", unpriced, "{}", nil, false)
	if err == nil || !strings.Contains(err.Error(), "--approve") || res != nil {
		t.Fatalf("an unapproved run the default policy asks about: res = %+v, err = %v", res, err)
	}
	if res, err := invokeModule("fake", unpriced, "{}", nil, true); err != nil || res == nil || !res.Envelope.OK {
		t.Fatalf("an approved run: res = %+v, err = %v", res, err)
	}
	if res, err := invokeModule("fake", "fake.echo", `{"name":"x"}`, nil, false); err != nil || res == nil || !res.Envelope.OK {
		t.Fatalf("a run the default allows: res = %+v, err = %v", res, err)
	}

	setPolicy(`{"rules":[{"tool":"fake__fake_estimate_unpriced","origin":["cli"],"action":"allow"}]}`)
	if res, err := invokeModule("fake", unpriced, "{}", nil, false); err != nil || res == nil || !res.Envelope.OK {
		t.Fatalf("a run a rule allows: res = %+v, err = %v", res, err)
	}

	setPolicy(`{"rules":[{"source":"module:fake","action":"deny"}]}`)
	if res, err := invokeModule("fake", "fake.echo", `{"name":"x"}`, nil, true); err == nil ||
		!strings.Contains(err.Error(), "denied") || res != nil {
		t.Fatalf("a run the policy denies: res = %+v, err = %v", res, err)
	}

	setPolicy(`{"default":"maybe"}`)
	if res, err := invokeModule("fake", "fake.echo", `{"name":"x"}`, nil, true); err == nil ||
		!strings.Contains(err.Error(), "approval policy") || res != nil {
		t.Fatalf("a run under an unreadable policy: res = %+v, err = %v", res, err)
	}
}
