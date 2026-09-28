package module

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/xibodev/compa/pkg/modproto"
)

// StageArtifact copies an artifact a module produced into the host's own
// artifact store and verifies its digest at the moment of transfer.
//
// NOT THE COMPOSITION MECHANISM, and this comment used to say it was.
//
// Chaining two modules is ordinary tool calling: the agent invokes one
// capability, receives an artifact path in the tool result, and passes it as an
// argument to the next. The reasoning driver handles that without host help,
// which is why nothing in the agent loop calls this function and nothing should
// be built to make it do so.
//
// What this exists for is the narrower case where a consumer needs a file under
// a root it actually holds. Root names are scoped to one invocation and are not
// portable: a file one module writes under its own rw root cannot be named by a
// root another was given, and neither should learn the other's layout. When
// that matters, the host copies the artifact somewhere neutral and supplies
// THAT as a read-only root.
//
// Its one caller today is the `handoff` CLI command, which prints numbered
// steps to demonstrate the transfer. That is a demonstration, not a product
// path.
//
// Verifying here rather than on read is deliberate: it is the one moment the
// host has both the producer's claim and the bytes, so a mismatch is caught
// before anything downstream can treat unverified content as provenanced.
//
// The digest checked here is Artifact.Digest, which answers "did these bytes
// survive the copy". It is NOT any digest a module carries inside its own
// payload: a real module's seed, for instance, has an evidence_digest covering
// evidence.jsonl alone, deliberately excluding prose so that regenerating a
// brief does not invalidate a consumer's provenance claim. Two values, two
// questions. Comparing one against the other would be a bug rather than a
// mismatch, so the host reads only the artifact's own digest and never looks
// inside the payload.
//
// A directory artifact -- a seed bundle is one -- is copied whole, and the
// digest is checked against the file the artifact actually points at.
func StageArtifact(req *modproto.Request, a modproto.Artifact, storeDir string) (string, error) {
	src, err := ResolveArtifact(req, a)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(src)
	if err != nil {
		return "", fmt.Errorf("artifact %q is not readable: %w", a.ID, err)
	}

	// Verify BEFORE copying, so unverified bytes never enter the store.
	if err := verifyDigest(src, a.Digest); err != nil {
		return "", err
	}

	dest := filepath.Join(storeDir, a.ID)
	root := src
	if !info.IsDir() {
		// A file artifact stages alongside its siblings, because a bundle's
		// manifest is meaningless without the files it references.
		root = filepath.Dir(src)
	}
	if err := copyTree(root, dest); err != nil {
		return "", err
	}
	return dest, nil
}

func verifyDigest(path, want string) error {
	if !modproto.ValidDigest(want) {
		return fmt.Errorf("artifact digest %q is malformed", want)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	blob, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if got := modproto.DigestSHA256(blob); got != want {
		return fmt.Errorf("artifact digest mismatch: module reported %s, contents hash to %s", want, got)
	}
	return nil
}

func copyTree(src, dest string) error {
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
		return os.WriteFile(target, blob, 0o644)
	})
}
