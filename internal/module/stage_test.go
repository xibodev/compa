package module_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/pkg/modproto"
)

// TestStagingVerifiesDigestAtTransfer proves the host checks a producer's claim
// against the actual bytes at the one moment it holds both.
//
// Staging is how a cross-module handoff happens: root names are scoped to one
// invocation, so a consumer can never name the producer's root. If the host
// copied without verifying, a consumer would receive content the producer's
// digest does not describe while believing it was provenanced.
func TestStagingVerifiesDigestAtTransfer(t *testing.T) {
	root := t.TempDir()
	body := []byte(`{"schema":"fake.seed/v1"}`)
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	req := &modproto.Request{Roots: map[string]modproto.Root{"out": {Path: root, Mode: "rw"}}}
	artifact := modproto.Artifact{
		ID: "seed", Kind: "fake.seed/v1",
		Path: "manifest.json", Root: "out",
		Digest: modproto.DigestSHA256(body),
	}

	// Honest digest: staged, and the whole bundle comes with it.
	staged, err := module.StageArtifact(req, artifact, filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("StageArtifact: %v", err)
	}
	if _, err := os.Stat(filepath.Join(staged, "manifest.json")); err != nil {
		t.Errorf("staged bundle is missing the artifact it points at: %v", err)
	}

	// Tampered content, digest unchanged: refused.
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), []byte(`{"schema":"evil"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "store2")
	if _, err := module.StageArtifact(req, artifact, dest); err == nil {
		t.Fatal("staging accepted content whose digest does not match the producer's claim")
	}
	// And nothing unverified was left behind.
	if _, err := os.Stat(dest); err == nil {
		t.Error("a refused artifact left content in the store")
	}
}

// TestStagingIgnoresPayloadInternalDigests pins a distinction that would
// silently corrupt provenance if it drifted.
//
// Artifact.Digest answers "did these bytes survive the copy". A module's own
// payload may carry a different digest answering a different question -- a
// real module's seed has an evidence_digest covering evidence.jsonl alone,
// deliberately excluding prose so regenerating a brief does not invalidate a
// consumer's provenance claim. Comparing one against the other would be a bug,
// not a mismatch, so staging must verify only the artifact's own digest.
func TestStagingIgnoresPayloadInternalDigests(t *testing.T) {
	root := t.TempDir()
	// A manifest carrying an internal digest that is deliberately WRONG for
	// the file's own bytes: it describes something else entirely.
	body := []byte(`{"schema":"fake.seed/v1","evidence_digest":"0000000000000000000000000000000000000000000000000000000000000000"}`)
	if err := os.WriteFile(filepath.Join(root, "manifest.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	req := &modproto.Request{Roots: map[string]modproto.Root{"out": {Path: root, Mode: "rw"}}}
	artifact := modproto.Artifact{
		ID: "seed", Kind: "fake.seed/v1",
		Path: "manifest.json", Root: "out",
		Digest: modproto.DigestSHA256(body), // honest for the FILE
	}

	if _, err := module.StageArtifact(req, artifact, filepath.Join(t.TempDir(), "store")); err != nil {
		t.Fatalf("staging must verify only the artifact's own digest, not one inside the payload: %v", err)
	}
}
