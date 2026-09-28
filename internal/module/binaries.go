package module

import (
	"os/exec"
	"path/filepath"

	"github.com/xibodev/compa/pkg/modproto"
)

// ResolveBinaries turns a module's DECLARED subprocess names into absolute
// paths for one invocation.
//
// Modules run with no inherited environment, so there is no PATH for them to
// search. The host resolves on their behalf, which is deliberately stronger
// than handing over a minimal PATH: a search can resolve to something that was
// never declared -- a shadowing entry, or a second binary with the same name in
// a supplied directory -- whereas an absolute path is an identity rather than a
// query. It also makes Permissions.Subprocess load-bearing: a binary the module
// did not declare is never supplied, so it cannot be run.
//
// Only names in `declared` are ever looked up. A name absent from the
// descriptor is not resolvable by asking, which is what keeps the capability
// list and the execution authority in agreement.
//
// A declared binary that cannot be resolved is OMITTED rather than mapped to an
// empty string, so "not supplied" and "supplied as nothing" stay
// distinguishable -- the same rule that keeps unknown cost from becoming zero.
// The unresolved names are returned so the host can tell the user which
// requirement is unmet instead of letting the module fail opaquely.
func ResolveBinaries(declared []string) (resolved map[string]string, missing []string) {
	resolved = make(map[string]string, len(declared))

	for _, name := range declared {
		// A declaration is a NAME, never a path. Accepting a path here would
		// let a module nominate any executable on the machine and have the
		// host bless it.
		if name == "" || name != filepath.Base(name) {
			missing = append(missing, name)
			continue
		}

		path, err := exec.LookPath(name)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			missing = append(missing, name)
			continue
		}
		resolved[name] = filepath.Clean(abs)
	}

	return resolved, missing
}

// GrantBinaries fills req.Binaries from the module's declared subprocess names,
// intersected with what the host could actually resolve.
//
// This is the per-invocation half of "installing a module grants nothing":
// authority to execute a binary is conferred here, on this call, and only for
// names the module declared up front.
func GrantBinaries(d *modproto.Descriptor, req *modproto.Request) (missing []string) {
	resolved, missing := ResolveBinaries(d.Permissions.Subprocess)
	req.Binaries = resolved
	return missing
}
