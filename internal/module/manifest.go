package module

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xibodev/compa/v4/pkg/fileutil"
	"github.com/xibodev/compa/v4/pkg/modproto"
)

// ManifestName is the file Install writes into a module's directory to record
// what it installed. Discovery skips dotfiles, so it is never run.
const ManifestName = ".compa-install.json"

// DisabledMarker is the file that turns an installed module off. It lives here
// rather than with the code that reads it because an upgrade carries it over.
const DisabledMarker = ".disabled"

// Manifest records one install: which module, in which directory, and the
// digest of the binary that was verified. A binary that no longer matches it
// was changed after the host checked it, and is not run until reinstalled.
type Manifest struct {
	ID     string `json:"id"`
	Dir    string `json:"dir"`
	SHA256 string `json:"sha256"`
}

// ReadManifest reads the manifest in a module directory. A directory without
// one returns nil and no error: a binary the owner put there by hand is theirs
// to run.
func ReadManifest(dir string) (*Manifest, error) {
	blob, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read install manifest: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(blob, &m); err != nil {
		return nil, fmt.Errorf("install manifest in %s is not readable: %w", filepath.Base(dir), err)
	}
	if !modproto.ValidDigest(m.SHA256) {
		return nil, fmt.Errorf("install manifest in %s carries no valid sha256 digest", filepath.Base(dir))
	}
	return &m, nil
}

func writeManifest(dir string, m Manifest) error {
	blob, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(filepath.Join(dir, ManifestName), append(blob, '\n'), 0o644)
}

// digestKey identifies one version of a file cheaply. A file whose size and
// modification time are unchanged is not read again.
type digestKey struct {
	size int64
	mod  time.Time
}

var digestCache = struct {
	sync.Mutex
	m map[string]digestCacheEntry
}{m: map[string]digestCacheEntry{}}

type digestCacheEntry struct {
	key    digestKey
	digest string
}

// FileDigest returns the protocol digest of a file's contents, re-reading it
// only when its size or modification time changed. Discovery checks every
// installed binary and declared document on each page load, and hashing a
// large binary each time is what made that slow.
func FileDigest(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key := digestKey{size: info.Size(), mod: info.ModTime()}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	digestCache.Lock()
	cached, ok := digestCache.m[abs]
	digestCache.Unlock()
	if ok && cached.key == key {
		return cached.digest, nil
	}

	digest, err := hashFile(path)
	if err != nil {
		return "", err
	}
	digestCache.Lock()
	digestCache.m[abs] = digestCacheEntry{key: key, digest: digest}
	digestCache.Unlock()
	return digest, nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return modproto.DigestPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

// verifyBinary refuses a binary whose contents differ from the digest recorded
// when it was installed.
func verifyBinary(path, want string) error {
	if want == "" {
		return nil
	}
	got, err := FileDigest(path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("%s: %s has changed since it was installed (installed %s, now %s);"+
			" it is not run until it is installed again", modproto.ErrHostModuleChanged,
			filepath.Base(path), shortDigest(want), shortDigest(got))
	}
	return nil
}
