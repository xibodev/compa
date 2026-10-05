package moduletools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/modproto"
)

// Progressive composition of module knowledge into the agent's prompt.
//
// The host composes an enabled module's knowledge in layers rather than
// concatenating its files into one opaque prompt:
//
//	1. host safety and identity instructions
//	2. concise capability summaries for every enabled module   <- always
//	3. the selected module's agent overlay                     <- on selection
//	4. only the skills selected for the current request        <- on selection
//	5. only schemas needed for the current call or artifact    <- per call
//
// Layer 2 is cheap and always present, so the agent knows what exists. Layers 3
// and 4 cost real context and load only when a user points the agent at a
// module -- the way selecting a connector in a chat box scopes what the model
// should reach for.
//
// Everything folded into agent context carries module ID, version, path and
// digest provenance, and content whose digest does not match what the module
// declared is REFUSED rather than loaded. A module's own documentation is
// untrusted input like anything else it produces.

// Knowledge is the module content composed into one turn.
type Knowledge struct {
	// Overlays are module-authored instruction documents, already verified.
	Overlays []Document
	// Skills are progressively loaded module knowledge, already verified.
	Skills []Document
	// Warnings record content that was declared but could not be used. They are
	// surfaced rather than swallowed: a silently missing overlay means the agent
	// behaves differently with no visible reason.
	Warnings []string
}

// Document is one verified piece of module knowledge.
type Document struct {
	ModuleID string
	Version  string
	ID       string
	Title    string
	Path     string
	Digest   string
	Tokens   int
	Content  string
}

// Provenance is the attribution line recorded for anything entering agent
// context, so a later reader can tell where an instruction came from.
func (d Document) Provenance() string {
	return fmt.Sprintf("module %s v%s · %s · %s", d.ModuleID, d.Version, d.Path, d.Digest)
}

// LoadKnowledge composes the selected module's overlay and skills.
//
// selectedModules is the user's explicit selection -- the connector gesture.
// When it is empty nothing is loaded, which is the point: an installed module
// costs one line of capability summary until someone asks for it.
//
// requestedSkills narrows further. When a capability is in play the host passes
// the skill IDs that capability declares, so a turn loads only the knowledge
// that turn needs.
func LoadKnowledge(installed []Installed, selectedModules, requestedSkills []string) Knowledge {
	var k Knowledge
	if len(selectedModules) == 0 {
		return k
	}

	selected := make(map[string]bool, len(selectedModules))
	for _, m := range selectedModules {
		selected[strings.ToLower(m)] = true
	}
	wanted := make(map[string]bool, len(requestedSkills))
	for _, s := range requestedSkills {
		wanted[s] = true
	}

	for _, in := range installed {
		// Runner is checked as well as Descriptor: this takes a caller-supplied
		// slice, and a half-populated Installed -- from a test, a future caller,
		// or a discovery path that changes -- would otherwise panic inside the
		// host rather than simply contributing nothing.
		if in.Descriptor == nil || in.Runner == nil ||
			!selected[strings.ToLower(in.Descriptor.Module)] {
			continue
		}
		// A disabled module contributes no knowledge, for the same reason it
		// contributes no tools.
		//
		// Loading the overlay while withholding the tools is the worst of both:
		// the model is told in detail how to do something, finds no tool for
		// it, and improvises. Observed doing exactly that -- with a module
		// disabled, selecting it in the chat box still produced the session data
		// its tools return, because the agent read the guidance and reached for
		// the shell.
		if in.Disabled {
			continue
		}
		d := in.Descriptor
		root := filepath.Dir(in.Runner.Binary)

		for _, o := range d.AgentOverlays {
			doc, err := loadVerified(root, d.Module, d.Version, o.ID, o.Title, o.Path, o.Digest, o.Tokens)
			if err != nil {
				k.Warnings = append(k.Warnings, err.Error())
				continue
			}
			k.Overlays = append(k.Overlays, doc)
		}

		for _, s := range d.Skills {
			// An empty request means "everything this module offers", which is
			// what a bare module selection implies. A non-empty one narrows to
			// the capability actually in play.
			if len(wanted) > 0 && !wanted[s.ID] {
				continue
			}
			doc, err := loadVerified(root, d.Module, d.Version, s.ID, s.Title, s.Path, s.Digest, s.Tokens)
			if err != nil {
				k.Warnings = append(k.Warnings, err.Error())
				continue
			}
			k.Skills = append(k.Skills, doc)
		}
	}
	return k
}

// loadVerified reads a module document and refuses it unless its contents match
// the digest the module declared.
//
// The digest is checked rather than recorded. A module's documentation becomes
// agent instructions, so loading content that does not match what was declared
// would mean the agent follows text nobody verified -- and the descriptor's
// digest would be decoration rather than provenance.
func loadVerified(root, moduleID, version, id, title, rel, digest string, tokens int) (Document, error) {
	if rel == "" {
		return Document{}, fmt.Errorf("module %s declares %q with no path", moduleID, id)
	}
	// Module paths are relative to the module root by contract; an absolute one
	// would escape the host's control over what it reads.
	if modproto.IsAbsolutePath(rel) {
		return Document{}, fmt.Errorf("module %s declares %q with an absolute path; paths must be module-relative", moduleID, id)
	}

	full := filepath.Clean(filepath.Join(root, filepath.FromSlash(rel)))
	// Not strings.HasPrefix: "/srv/modules/acme-evil" carries the prefix
	// "/srv/modules/acme" and is a different module's directory. A module
	// declaring "../acme-evil/overlay.md" escaped its own tree and the prefix
	// check accepted it.
	if !module.WithinRoot(filepath.Clean(root), full) {
		return Document{}, fmt.Errorf("module %s declares %q outside its own directory", moduleID, id)
	}

	if info, err := os.Stat(full); err == nil && info.Size() > maxDocumentBytes {
		return Document{}, fmt.Errorf("module %s declares %q at %s, which is larger than %d KiB and is not loaded",
			moduleID, id, rel, maxDocumentBytes>>10)
	}
	blob, err := os.ReadFile(full)
	if err != nil {
		return Document{}, fmt.Errorf("module %s declares %q at %s, which is not readable", moduleID, id, rel)
	}

	if got := modproto.DigestSHA256(blob); got != digest {
		return Document{}, fmt.Errorf(
			"module %s declares %q with digest %s but the file hashes to %s; refusing to load unverified content into agent context",
			moduleID, id, digest, got)
	}
	// The declared token count is the context cost the module promised, and
	// what selection budgets with; content well beyond it is not loaded.
	if limit := declaredBytes(tokens); limit > 0 && len(blob) > limit {
		return Document{}, fmt.Errorf("module %s declares %q at %s as %d tokens, but it holds %d bytes,"+
			" more than that allows; it is not loaded", moduleID, id, rel, tokens, len(blob))
	}

	return Document{
		ModuleID: moduleID, Version: version, ID: id, Title: title,
		Path: rel, Digest: digest, Tokens: tokens,
		Content: string(blob),
	}, nil
}

// Compose renders loaded knowledge as prompt sections.
//
// Each document carries its provenance inline, so a reader of the assembled
// prompt can tell which module supplied which instruction and verify it.
func (k Knowledge) Compose() (overlays string, skills string) {
	var ob, sb strings.Builder

	for _, d := range k.Overlays {
		fmt.Fprintf(&ob, "<!-- %s -->\n%s\n\n", d.Provenance(), strings.TrimSpace(d.Content))
	}
	for _, d := range k.Skills {
		fmt.Fprintf(&sb, "<!-- %s -->\n%s\n\n", d.Provenance(), strings.TrimSpace(d.Content))
	}
	return strings.TrimSpace(ob.String()), strings.TrimSpace(sb.String())
}

// Tokens is the declared context cost of everything loaded, so the host can
// budget before composing rather than discovering the cost afterwards.
func (k Knowledge) Tokens() int {
	total := 0
	for _, d := range k.Overlays {
		total += d.Tokens
	}
	for _, d := range k.Skills {
		total += d.Tokens
	}
	return total
}

// KnowledgeLoader returns a function that composes one module's knowledge.
//
// Discovery happens ONCE, here, because describing every installed module is a
// subprocess per module and a turn must not pay that. The returned closure is
// called per turn with whatever module the user selected.
//
// It returns empty for an empty selection, which is the layered-composition
// rule made concrete: an installed module costs one line of capability summary
// until someone points the agent at it, and only then do its overlay and skills
// enter context.
//
// Returns nil when nothing is installed, so a caller can tell "no modules" from
// "a module that contributes nothing".
func KnowledgeLoader(installed []Installed) func(string) (string, string, []string) {
	if len(installed) == 0 {
		return nil
	}
	known := make(map[string]bool, len(installed))
	disabled := make(map[string]bool, len(installed))
	dirs := make(map[string]string, len(installed))
	for _, in := range installed {
		if in.Descriptor != nil {
			id := strings.ToLower(in.Descriptor.Module)
			known[id] = true
			disabled[id] = in.Disabled
			if in.Runner != nil {
				dirs[id] = filepath.Dir(in.Runner.Binary)
			}
		}
	}

	return func(moduleID string) (string, string, []string) {
		id := strings.TrimSpace(moduleID)
		if id == "" {
			return "", "", nil
		}
		if !known[strings.ToLower(id)] {
			// A selection naming something not installed is REPORTED rather
			// than treated as "a module that contributes nothing". The two are
			// indistinguishable in the return value and mean opposite things:
			// one is a module with no overlay, the other is a name the host
			// cannot honour at all.
			//
			// It matters because the host states the selection to the agent as
			// an instruction. Telling a model to prefer the capabilities of a
			// module that does not exist is an instruction it can only follow
			// by improvising, which is the behaviour this whole boundary exists
			// to prevent.
			return "", "", []string{fmt.Sprintf(
				"module %q was selected but is not installed; no module knowledge was loaded", id)}
		}
		// The marker is read again at selection time: the module may have been
		// disabled after the gateway started, and the startup snapshot alone
		// kept loading its guidance until a restart.
		dir := dirs[strings.ToLower(id)]
		if disabled[strings.ToLower(id)] || dir != "" && Disabled(dir) {
			// Installed but turned off. Distinct from "not installed", because
			// the user can act on it -- and the agent must say WHICH, or the
			// user is told to install something they already have.
			return "", "", []string{fmt.Sprintf(
				"module %q is installed but DISABLED, so it contributes no"+
					" capabilities and no guidance; it can be re-enabled on the"+
					" Modules page", id)}
		}
		k := LoadKnowledge(installed, []string{id}, nil)
		overlays, skills := k.Compose()
		warnings := k.Warnings
		// What enters the prompt is bounded whatever the module declared: the
		// declared token counts are the module's own claim. Skills go first,
		// since module_knowledge can still load them one at a time.
		if len(overlays)+len(skills) > maxPromptKnowledge {
			skills = ""
			warnings = append(warnings, fmt.Sprintf("module %q skills were left out of the prompt because"+
				" its guidance exceeds %d KiB; load one with module_knowledge", id, maxPromptKnowledge>>10))
		}
		if len(overlays) > maxPromptKnowledge {
			overlays = ""
			warnings = append(warnings, fmt.Sprintf("module %q overlay was left out of the prompt because"+
				" it exceeds %d KiB", id, maxPromptKnowledge>>10))
		}
		return overlays, skills, warnings
	}
}

// maxPromptKnowledge bounds the module guidance one selection adds to the
// prompt, and maxDocumentBytes the size of one overlay or skill.
const (
	maxPromptKnowledge = 64 << 10
	maxDocumentBytes   = 64 << 10
)

// declaredBytes is the most content a declared token count allows: about 4
// bytes a token, doubled because the count is an estimate. It is 0, meaning
// only maxDocumentBytes applies, when the module declared no count.
func declaredBytes(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * 4 * 2
}

// KnownModules reports which module ids are installed, lowercased.
//
// Exposed so a caller that must decide whether to ACT on a selection -- rather
// than merely load its knowledge -- can ask without repeating discovery.
func KnownModules(installed []Installed) map[string]bool {
	known := make(map[string]bool, len(installed))
	for _, in := range installed {
		if in.Descriptor != nil {
			known[strings.ToLower(in.Descriptor.Module)] = true
		}
	}
	return known
}
