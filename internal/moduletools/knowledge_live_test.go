package moduletools

import (
	"context"
	"os"
	"testing"
)

// liveCompaHome returns $COMPA_HOME, the Compa home whose installed modules
// the live tests read; they skip without one, or without modules there.
func liveCompaHome(t *testing.T) string {
	t.Helper()
	home := os.Getenv("COMPA_HOME")
	if home == "" {
		t.Skip("COMPA_HOME is not set")
	}
	if _, err := os.Stat(ModulesDir(home)); err != nil {
		t.Skip("no modules installed")
	}
	return home
}

// TestLoadKnowledgeAgainstRealModules exercises composition against whatever
// modules are actually installed, and skips when none are. It is the check that
// a declared overlay is genuinely readable and genuinely matches its digest --
// which a synthetic fixture cannot tell you, because the fixture author
// controls both sides.
//
// IT HAD NO ASSERTIONS AT ALL, AND THAT IS WORSE THAN IT SOUNDS. Every outcome
// printed and passed: a module declaring five overlays and loading zero read
// exactly like one that loaded all five. The comment above claims this is "the
// check that a declared overlay is genuinely readable and genuinely matches its
// digest" -- and it checked nothing. A digest mismatch REFUSES content silently
// by design (see staleContentWarnings), so the failure this test exists to
// catch is invisible precisely when it happens.
//
// Found by running a sibling lane's audit method against my own tree after I
// made the same mistake in a different test and they found the same class in
// theirs. Their finding: grouping by file or by function finds nothing, because
// the file has assertions and the function has assertions -- you have to read
// what each log actually REPORTS. Mine did not even need that: the function has
// zero assertions and my pass-1 grep still missed it, because it greps for
// t.Error and this package's other tests use it while this function does not.
func TestLoadKnowledgeAgainstRealModules(t *testing.T) {
	home := liveCompaHome(t)

	installed := Discover(context.Background(), home)
	if len(installed) == 0 {
		t.Skip("no modules discovered")
	}

	for _, in := range installed {
		if in.Descriptor == nil {
			continue
		}
		id := in.Descriptor.Module
		k := LoadKnowledge(installed, []string{id}, nil)

		declared := len(in.Descriptor.AgentOverlays) + len(in.Descriptor.Skills)
		loaded := len(k.Overlays) + len(k.Skills)
		t.Logf("%s: declared %d, loaded %d, tokens %d, warnings %d",
			id, declared, loaded, k.Tokens(), len(k.Warnings))
		// ASSERT, do not merely print. A warning here means declared content
		// was refused -- unreadable, or its bytes no longer match the digest the
		// module published. That content then never reaches agent context, and
		// the ONLY other symptom is guidance quietly not appearing.
		for _, w := range k.Warnings {
			t.Errorf("%s: declared content was refused at load time: %s", id, w)
		}

		// A module that declares content and loads NONE of it is the case this
		// test was written for, and it was the case that printed and passed.
		if declared > 0 && loaded == 0 {
			t.Errorf("%s declares %d overlays/skills and loaded ZERO. Either every"+
				" digest is stale or the load path is broken; both are invisible in"+
				" normal use because refused content simply never appears", id, declared)
		}

		// Loaded content must be non-empty. A zero-token overlay passed the
		// digest check and contributes nothing, which is a different failure
		// from being refused and would otherwise look identical to success.
		for _, doc := range k.Overlays {
			if doc.Tokens == 0 {
				t.Errorf("%s: overlay %q loaded with zero tokens", id, doc.ID)
			}
		}
		for _, d := range k.Overlays {
			t.Logf("  overlay %s (%d tok) %s", d.ID, d.Tokens, d.Provenance())
		}
		for _, d := range k.Skills {
			t.Logf("  skill   %s (%d tok)", d.ID, d.Tokens)
		}
	}
}

// TestSelectionGatesLoading is the property the connector gesture depends on:
// an installed module costs nothing in context until a user selects it.
func TestSelectionGatesLoading(t *testing.T) {
	installed := Discover(context.Background(), liveCompaHome(t))

	if k := LoadKnowledge(installed, nil, nil); k.Tokens() != 0 {
		t.Errorf("no selection loaded %d tokens; an unselected module must cost nothing", k.Tokens())
	}
	if k := LoadKnowledge(installed, []string{"does-not-exist"}, nil); k.Tokens() != 0 {
		t.Error("selecting an uninstalled module loaded content")
	}
}
