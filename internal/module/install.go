package module

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/xibodev/compa/pkg/modproto"
)

// Install registers a module binary with this host.
//
// Registration is a HOST action, deliberately. A module never writes into the
// host's state directory and never needs to know its layout: it hands the host
// a path and the host decides everything else. That keeps the confinement rule
// intact -- a module that could write into host state could grant itself
// authority -- and it means a module installer only has to find `compa-kernel`
// on PATH.
//
// The order matters:
//
//  1. describe and VALIDATE before copying, so a binary that does not speak the
//     protocol is refused rather than installed and discovered broken later;
//  2. derive the module ID from the DESCRIPTOR, never from the filename or the
//     caller, so a binary cannot be installed under a name it does not claim;
//  3. copy into a host-chosen path;
//  4. re-describe the INSTALLED copy, so what was verified is what will run.
//
// It is idempotent: installing over an existing module replaces it, which is
// how upgrade works.
func Install(ctx context.Context, home, binaryPath string) (string, error) {
	abs, err := filepath.Abs(binaryPath)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", binaryPath, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("no module binary at %q", binaryPath)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory; supply the module executable", binaryPath)
	}

	// 1. Verify before touching anything.
	probe := &Runner{Binary: abs}
	d, _, err := probe.Describe(ctx)
	if err != nil {
		// A timeout is not a protocol failure, and saying so sends the author
		// to the wrong place. Observed: a module that describes itself in ~1s
		// took 11.8s once, immediately after its build wrote the file -- almost
		// certainly the virus scanner reading 13MB -- and the install refused
		// with "does not speak the module protocol". It spoke it perfectly; it
		// was slow once, and the retry succeeded.
		//
		// The distinction matters because the two need opposite responses:
		// fix your module, versus run it again.
		if errors.Is(err, context.DeadlineExceeded) ||
			strings.Contains(err.Error(), modproto.ErrHostTimeout) {
			return "", fmt.Errorf(
				"%q did not describe itself within the deadline: %w\n"+
					"This is a timeout, not a protocol error. A large binary can"+
					" be slow on its first run while a virus scanner reads it;"+
					" try again before changing anything",
				filepath.Base(abs), err)
		}
		return "", fmt.Errorf("%q does not speak the module protocol: %w", filepath.Base(abs), err)
	}
	if d.Module == "" {
		return "", fmt.Errorf("%q describes itself without a module ID", filepath.Base(abs))
	}
	// 2. Identity comes from the descriptor.
	id := d.Module
	if id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("module ID %q is not a single path segment", id)
	}

	// 3. Host chooses the destination.
	destDir := filepath.Join(ModulesDir(home), id)
	// Whether something was already installed here decides what cleanup may
	// remove: this command's own mess, never a working install it replaced.
	replacedExisting := false
	if entries, readErr := os.ReadDir(destDir); readErr == nil && len(entries) > 0 {
		replacedExisting = true
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	name := id
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	dest := filepath.Join(destDir, name)

	// Installing a module from its own installed copy is a re-registration, not
	// a copy. Attempting it produced a bare "Access is denied" -- Windows will
	// not rename a file onto itself while it is the running source -- which a
	// module author reasonably read as the host being broken.
	//
	// The verification below still runs, so a re-register genuinely re-checks
	// the module rather than trusting that it was fine last time.
	sameFile := false
	if destInfo, statErr := os.Stat(dest); statErr == nil {
		sameFile = os.SameFile(info, destInfo)
	}

	if !sameFile {
		if err := copyExecutable(abs, dest); err != nil {
			return "", fmt.Errorf("install %s: %w", id, err)
		}
	}

	// A module's declared overlays and skills travel WITH the binary.
	//
	// They are declared as module-relative paths, so without copying them the
	// installed module would describe knowledge the host cannot read -- and the
	// failure is quiet: the agent simply behaves as if the module documented
	// nothing. Missing content is reported rather than fatal, because a module
	// whose binary works is still useful.
	warnings := copyDeclaredContent(filepath.Dir(abs), destDir, d)

	// A module's declared REQUIREMENTS may name a directory it ships beside its
	// binary -- a renderer's composition bundle, a pack's templates. Those are
	// resolved relative to the module's working directory at run time, so
	// without them an installed module reports a dependency it actually shipped
	// with.
	//
	// Only requirements the module DECLARED are considered, and only when the
	// named directory exists beside the source binary. The host never goes
	// looking for undeclared content: what a module declares is what the host
	// carries, and nothing else.
	warnings = append(warnings, copyDeclaredRuntimeDirs(filepath.Dir(abs), destDir, d)...)

	// 4. Confirm the installed copy behaves like the one that was verified.
	installed := &Runner{Binary: dest, ModuleID: id}
	if _, _, err := installed.Describe(ctx); err != nil {
		// Remove ONLY what this command created. A module author lost a working
		// install to this line: their re-register failed, and the cleanup took
		// the previous, working copy with it. Replacing something that worked
		// with nothing is worse than refusing.
		if !replacedExisting {
			os.RemoveAll(destDir)
			return "", fmt.Errorf("installed copy of %s failed verification and was removed: %w", id, err)
		}
		return "", fmt.Errorf(
			"installed copy of %s failed verification: %w\n"+
				"the previous install was left in place; remove it explicitly if"+
				" you want it gone", id, err)
	}

	if len(warnings) > 0 {
		return id, &PartialInstall{Module: id, Warnings: warnings}
	}
	return id, nil
}

// PartialInstall reports a module that installed and runs, but whose declared
// knowledge could not be copied.
//
// It is an error type rather than a silent warning because the consequence is
// invisible at runtime: the agent behaves as though the module documented
// nothing, with no signal that anything is missing.
type PartialInstall struct {
	Module   string
	Warnings []string
}

func (e *PartialInstall) Error() string {
	return fmt.Sprintf("module %s installed, but some declared content was not copied: %s",
		e.Module, strings.Join(e.Warnings, "; "))
}

// copyDeclaredContent copies the overlays and skills a descriptor declares,
// resolving them relative to the source binary's directory.
//
// Every path is confined to the source root: a module declaring "../../secrets"
// must not cause the host to copy something outside the module's own tree.
func copyDeclaredContent(srcRoot, destRoot string, d *modproto.Descriptor) []string {
	var warnings []string

	copyOne := func(kind, id, rel, declaredDigest string) {
		if rel == "" {
			return
		}
		if modproto.IsAbsolutePath(rel) {
			warnings = append(warnings, fmt.Sprintf("%s %q declares an absolute path", kind, id))
			return
		}
		src := filepath.Clean(filepath.Join(srcRoot, filepath.FromSlash(rel)))
		if !withinRoot(filepath.Clean(srcRoot), src) {
			warnings = append(warnings, fmt.Sprintf("%s %q declares a path outside the module tree", kind, id))
			return
		}
		blob, err := os.ReadFile(src)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s %q at %s was not readable", kind, id, rel))
			return
		}
		// Verify the digest HERE, while a person is watching.
		//
		// Content is digest-checked again when it is loaded into agent context,
		// and content that fails is refused there -- correctly, since a
		// module's documentation is untrusted input. But that refusal is
		// silent from the user's point of view: the module installs cleanly,
		// its overlay simply never appears in any turn, and the agent behaves
		// differently with no visible reason.
		//
		// Observed: a module edited its overlay without rebuilding, so the
		// declared digest described content it no longer shipped. Install
		// reported zero warnings and the overlay never loaded again.
		//
		// This does not refuse the install -- a stale digest is a mistake in
		// the module, not a reason to withhold its capabilities -- but it says
		// so at the moment someone can act on it.
		if declaredDigest != "" {
			actual := modproto.DigestSHA256(blob)
			if actual != declaredDigest {
				warnings = append(warnings, fmt.Sprintf(
					"%s %q at %s does not match its declared digest, so it will be"+
						" REFUSED when loaded into agent context (declared %s, found %s);"+
						" the module likely changed this file without rebuilding",
					kind, id, rel, shortDigest(declaredDigest), shortDigest(actual)))
			}
		}

		dst := filepath.Join(destRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s %q: %v", kind, id, err))
			return
		}
		if err := os.WriteFile(dst, blob, 0o644); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s %q: %v", kind, id, err))
		}
	}

	for _, o := range d.AgentOverlays {
		copyOne("overlay", o.ID, o.Path, o.Digest)
	}
	for _, sk := range d.Skills {
		copyOne("skill", sk.ID, sk.Path, sk.Digest)
	}
	return warnings
}

// copyDeclaredRuntimeDirs copies directories a module declares as "directory"
// or "runtime" requirements, when they sit beside the source binary.
//
// This is deliberately narrow. A module that needs a runtime directory says so
// in its descriptor, and the host copies exactly that -- it does not scan for
// likely-looking folders, and it does not follow paths outside the module's own
// tree. A requirement naming something absent is reported rather than assumed
// harmless, because the failure is otherwise a confusing "missing dependency"
// for something the module believed it shipped.
func copyDeclaredRuntimeDirs(srcRoot, destRoot string, d *modproto.Descriptor) []string {
	var warnings []string

	for _, req := range d.Requirements {
		if (req.Kind != "directory" && req.Kind != "runtime") || req.Name == "" {
			continue
		}
		rel := req.Name
		// Stricter than the shared absolute check on purpose: a runtime
		// DIRECTORY is copied wholesale, so ".." anywhere in it would pull in a
		// tree the module never declared, and ":" catches a drive letter the
		// host-specific IsAbs would miss on Linux. The shared check is used
		// first so the platform-independent rule cannot drift away from the
		// other three call sites.
		if modproto.IsAbsolutePath(rel) || filepath.IsAbs(rel) ||
			strings.ContainsAny(rel, `:`) || strings.Contains(rel, "..") {
			warnings = append(warnings, fmt.Sprintf("requirement %q is not a module-relative directory", req.Name))
			continue
		}

		src := filepath.Clean(filepath.Join(srcRoot, filepath.FromSlash(rel)))
		if !withinRoot(filepath.Clean(srcRoot), src) {
			warnings = append(warnings, fmt.Sprintf("requirement %q resolves outside the module tree", req.Name))
			continue
		}
		info, err := os.Stat(src)
		if err != nil || !info.IsDir() {
			warnings = append(warnings, fmt.Sprintf("required directory %q was not found beside the module binary", req.Name))
			continue
		}
		if err := copyDir(src, filepath.Join(destRoot, filepath.FromSlash(rel))); err != nil {
			warnings = append(warnings, fmt.Sprintf("required directory %q: %v", req.Name, err))
		}
	}
	return warnings
}

func copyDir(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		blob, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, blob, info.Mode().Perm())
	})
}

// Remove uninstalls a module and deletes its binary.
//
// A module's own state under <home>/state/<id>/ is deliberately left alone:
// uninstalling should not destroy a user's work, and reinstalling should find
// it again. Removing state is a separate, explicit action.
func Remove(home, id string) error {
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid module ID %q", id)
	}
	dir := filepath.Join(ModulesDir(home), id)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("module %q is not installed", id)
	}
	return os.RemoveAll(dir)
}

// ModulesDir is where installed modules live under the host state root.
func ModulesDir(home string) string { return filepath.Join(home, "modules") }

func copyExecutable(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	// Replace rather than truncate: on Windows an executable that is currently
	// running cannot be overwritten, and a failed partial write would leave a
	// corrupt module installed.
	tmp := dest + ".installing"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}

	os.Remove(dest) // ignore: absent is fine, in-use surfaces on rename
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("could not replace %s (is it running?): %w", filepath.Base(dest), err)
	}
	return nil
}

// DescribeInstalled returns the descriptor of one installed module.
func DescribeInstalled(ctx context.Context, home, id string) (*modproto.Descriptor, error) {
	name := id
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	r := &Runner{Binary: filepath.Join(ModulesDir(home), id, name), ModuleID: id}
	d, _, err := r.Describe(ctx)
	return d, err
}

// shortDigest abbreviates a digest for a message a person reads.
func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19] + "…"
	}
	return d
}
