package moduletools

import (
	"fmt"
	"os"
	"strings"
)

// EnvApproveCapabilities names capabilities the operator has already decided
// about, as a comma-separated list of "<module>/<capability>".
//
// It exists because enforcing the approval guarantee made the operator's actual
// goal impossible. "Create a video from simple chatting" goes through a
// cost_known:false capability every time; the agent cannot approve, because an
// approval arriving through the model is stripped by design; and approval is
// per-call and remembered nowhere. So chat could never complete that journey
// again -- the control was correct and the product stopped working.
//
// This is the operator saying, once and explicitly, "I have read what this
// capability declares and I accept it". It is not the host inferring anything,
// and it is not the model deciding: a standing approval can only be set by
// someone with access to the environment the host runs in.
//
// Deliberately NOT a wildcard. "Approve everything this module ever adds" is
// not a decision anyone can make in advance -- the module updates and the
// approval silently covers capabilities that did not exist when it was
// written. Each entry names one capability.
const EnvApproveCapabilities = "COMPA_APPROVE_CAPABILITIES"

// PreApproved reports whether the operator has standing approval on file for
// exactly this module and capability.
//
// Unset means unchanged: everything that may bill still gates. A host that
// stopped gating because a variable was absent would turn the guarantee off for
// everyone who never sets it.
func PreApproved(moduleID, capabilityID string) bool {
	want := strings.ToLower(moduleID + "/" + capabilityID)
	for _, entry := range standingEntries() {
		if strings.ToLower(entry) == want {
			return true
		}
	}
	return false
}

// StandingApprovalWarnings reports entries the host could not use.
//
// A malformed entry must be VISIBLE. Ignoring it silently leaves the operator
// believing they approved something they did not, and they only discover
// otherwise when the agent refuses -- at which point the setting looks broken
// rather than mistyped.
func StandingApprovalWarnings() []string {
	var out []string
	for _, entry := range standingEntries() {
		module, capability, found := strings.Cut(entry, "/")
		switch {
		case !found:
			out = append(out, fmt.Sprintf(
				"%s entry %q names no capability; the form is"+
					" <module>/<capability>, and this approves nothing",
				EnvApproveCapabilities, entry))
		case strings.TrimSpace(module) == "" || strings.TrimSpace(capability) == "":
			out = append(out, fmt.Sprintf(
				"%s entry %q has an empty module or capability, and approves"+
					" nothing", EnvApproveCapabilities, entry))
		case strings.Contains(entry, "*"):
			out = append(out, fmt.Sprintf(
				"%s entry %q uses a wildcard, which is refused: approving"+
					" everything a module MIGHT add later is not a decision"+
					" that can be made in advance. Name each capability",
				EnvApproveCapabilities, entry))
		}
	}
	return out
}

// standingEntries splits the variable, dropping empties so a stray comma or
// trailing separator in a shell profile is tolerated rather than silently
// producing nothing.
func standingEntries() []string {
	raw := strings.TrimSpace(os.Getenv(EnvApproveCapabilities))
	if raw == "" {
		return nil
	}
	var out []string
	for _, entry := range strings.Split(raw, ",") {
		if e := strings.TrimSpace(entry); e != "" {
			out = append(out, e)
		}
	}
	return out
}
