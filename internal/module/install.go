package module

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/xibodev/compa/v2/pkg/modproto"
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
//  3. copy everything into a staging directory beside the install;
//  4. re-describe the STAGED copy, so what was verified is what will run, and
//     record its digest in an install manifest;
//  5. swap the staged directory into place, keeping the previous install until
//     the swap has succeeded.
//
// Nothing an earlier install left is touched before step 5, so a failed
// upgrade leaves the working module exactly as it was. Installs of the same
// module are serialised, across processes too, so the CLI and the dashboard
// cannot interleave.
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

	// 2. Identity comes from the descriptor, held to the one ID grammar.
	id := d.Module
	destDir, err := InstallDir(home, id)
	if err != nil {
		return "", fmt.Errorf("%q describes itself as a module the host cannot install: %w",
			filepath.Base(abs), err)
	}
	root := filepath.Dir(destDir)

	unlock, err := lockModule(home, id)
	if err != nil {
		return "", err
	}
	defer unlock()
	defer InvalidateDescribeCache()

	// 3. Stage. The staging directory is a dotfile beside the install, so
	// discovery never runs it and the final rename stays on one filesystem.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(root, ".install-"+id+"-")
	if err != nil {
		return "", fmt.Errorf("install %s: %w", id, err)
	}
	staged := false
	defer func() {
		if !staged {
			_ = os.RemoveAll(stage)
		}
	}()

	stagedBinary := filepath.Join(stage, BinaryName(id))
	if err := copyExecutable(abs, stagedBinary); err != nil {
		return "", fmt.Errorf("install %s: %w", id, err)
	}

	// A module's declared overlays and skills travel WITH the binary.
	//
	// They are declared as module-relative paths, so without copying them the
	// installed module would describe knowledge the host cannot read -- and the
	// failure is quiet: the agent simply behaves as if the module documented
	// nothing. Missing content is reported rather than fatal, because a module
	// whose binary works is still useful.
	srcRoot := filepath.Dir(abs)
	warnings := copyDeclaredContent(srcRoot, stage, d)

	// A module's declared REQUIREMENTS may name a directory it ships beside its
	// binary -- a renderer's composition bundle, a pack's templates. The module
	// reaches them through its bundle root, so without them an installed
	// module reports a dependency it actually shipped with.
	//
	// Only requirements the module DECLARED are considered, and only when the
	// named directory exists beside the source binary. The host never goes
	// looking for undeclared content: what a module declares is what the host
	// carries, and nothing else.
	warnings = append(warnings, copyDeclaredRuntimeDirs(srcRoot, stage, d, root)...)

	// 4. Confirm the staged copy behaves like the one that was verified.
	verify := &Runner{Binary: stagedBinary, ModuleID: id, WorkDir: ScratchDir(home, id)}
	if _, _, err := verify.Describe(ctx); err != nil {
		return "", fmt.Errorf("the copy of %s failed verification, so nothing was changed: %w", id, err)
	}
	digest, err := FileDigest(stagedBinary)
	if err != nil {
		return "", fmt.Errorf("install %s: %w", id, err)
	}
	if err := writeManifest(stage, Manifest{ID: id, Dir: id, SHA256: digest}); err != nil {
		return "", fmt.Errorf("install %s: %w", id, err)
	}
	// A module the user turned off stays off when it is upgraded.
	if exists(filepath.Join(destDir, DisabledMarker)) {
		if err := copyFile(filepath.Join(destDir, DisabledMarker), filepath.Join(stage, DisabledMarker), 0o644); err != nil {
			return "", fmt.Errorf("install %s: %w", id, err)
		}
	}

	// 5. Swap.
	if err := swapInto(destDir, stage); err != nil {
		return "", fmt.Errorf("install %s: %w; the previous install was left in place", id, err)
	}
	staged = true

	if len(warnings) > 0 {
		return id, &PartialInstall{Module: id, Warnings: warnings}
	}
	return id, nil
}

// swapInto replaces dest with the staged directory. The previous install is
// moved aside first and restored if the staged one cannot take its place, so
// at every moment either the old or the new install is complete.
func swapInto(dest, stage string) error {
	old := ""
	if exists(dest) {
		old = sideName(dest, ".old-")
		if err := renameRetry(dest, old); err != nil {
			return fmt.Errorf("could not move the installed copy aside (is it running?): %w", err)
		}
	}
	if err := renameRetry(stage, dest); err != nil {
		if old != "" {
			_ = renameRetry(old, dest)
		}
		return fmt.Errorf("could not move the new copy into place: %w", err)
	}
	if old != "" {
		// Best effort: the old copy is inert once it is out of the way, and a
		// leftover is a dotfile discovery never runs.
		_ = os.RemoveAll(old)
	}
	return nil
}

// sideName is a hidden, unique name beside path, for a directory on its way
// in or out.
func sideName(path, prefix string) string {
	var buf [6]byte
	_, _ = rand.Read(buf[:])
	return filepath.Join(filepath.Dir(path), prefix+filepath.Base(path)+"-"+hex.EncodeToString(buf[:]))
}

// renameRetry renames, retrying briefly on Windows, where a file that was just
// written or run can stay locked for a moment while a scanner or the loader
// lets go of it.
func renameRetry(from, to string) error {
	attempts := 1
	if runtime.GOOS == "windows" {
		attempts = 20
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return err
		}
		time.Sleep(time.Duration(i+1) * 10 * time.Millisecond)
	}
	return err
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
		if !withinRoot(filepath.Clean(srcRoot), src) || src == filepath.Clean(srcRoot) {
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
//
// modulesRoot is the host's modules directory, which a declared directory may
// not contain: copying it would copy every installed module, and the staging
// directory inside it, into this one.
func copyDeclaredRuntimeDirs(srcRoot, destRoot string, d *modproto.Descriptor, modulesRoot string) []string {
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
		// "." names the binary's own folder, which would copy whatever else
		// happens to sit beside it -- a whole download folder, a source tree.
		if src == filepath.Clean(srcRoot) {
			warnings = append(warnings, fmt.Sprintf(
				"requirement %q names the folder the module binary is in; declare the subdirectory it needs", req.Name))
			continue
		}
		if !withinRoot(filepath.Clean(srcRoot), src) {
			warnings = append(warnings, fmt.Sprintf("requirement %q resolves outside the module tree", req.Name))
			continue
		}
		if modulesRoot != "" && withinRoot(src, filepath.Clean(modulesRoot)) {
			warnings = append(warnings, fmt.Sprintf(
				"requirement %q contains the host's modules directory and was not copied", req.Name))
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
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

// Remove uninstalls a module and deletes its binary.
//
// A module's own state under <home>/state/<id>/ is deliberately left alone:
// uninstalling should not destroy a user's work, and reinstalling should find
// it again. Removing state is a separate, explicit action.
//
// The ID is checked against the one ID grammar and the target must be a
// direct child of the modules directory. The check this replaces accepted
// "..", and Remove("..") deleted the whole Compa home.
func Remove(home, id string) error {
	dir, err := InstallDir(home, id)
	if err != nil {
		return err
	}
	unlock, err := lockModule(home, id)
	if err != nil {
		return err
	}
	defer unlock()
	defer InvalidateDescribeCache()

	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("module %q is not installed", id)
	}
	if !info.IsDir() {
		// A link, a junction or a stray file where a module directory belongs:
		// remove the entry itself, never what it points at.
		return os.Remove(dir)
	}

	// Moved aside first, so the module disappears at once and completely
	// even when Windows will not delete a file that is still in use; what
	// cannot be deleted then is a dotfile discovery never runs.
	trash := sideName(dir, ".removing-")
	if !isDirectChild(filepath.Dir(dir), trash) {
		return fmt.Errorf("refusing to remove %s", dir)
	}
	if err := renameRetry(dir, trash); err != nil {
		return fmt.Errorf("could not remove module %q (is it running?): %w", id, err)
	}
	if err := os.RemoveAll(trash); err != nil {
		return fmt.Errorf("module %q was removed, but some of its files could not be deleted from %s: %w",
			id, trash, err)
	}
	return nil
}

func copyExecutable(src, dest string) error {
	return copyFile(src, dest, 0o755)
}

func copyFile(src, dest string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// DescribeInstalled returns the descriptor of one installed module.
func DescribeInstalled(ctx context.Context, home, id string) (*modproto.Descriptor, error) {
	r, err := NewRunner(home, id)
	if err != nil {
		return nil, err
	}
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
