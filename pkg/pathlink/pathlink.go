// Package pathlink resolves a filesystem path to its true location, following
// every kind of link the host supports.
//
// It exists because filepath.EvalSymlinks is not enough on Windows, and two
// separate confinement checks in this codebase were relying on it: the module
// artifact boundary and the agent's own workspace boundary. Both could be
// walked through with a junction. Two copies of a security decision is how one
// of them gets fixed and the other does not, so there is now one.
package pathlink

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxHops bounds link following, so a cycle terminates rather than hanging.
const maxHops = 40

// Resolve returns the true location of path.
//
// A Windows junction (mklink /J) is invisible to filepath.EvalSymlinks: asked
// about the junction itself it returns the junction's own path unchanged, and
// asked about a file THROUGH one it fails with ErrNotExist -- on a path
// os.Stat opens happily. A junction needs no privilege to create, unlike a
// symlink, so it is the easiest boundary to cross and the one that was not
// checked.
//
// A path that does not exist resolves to itself with no error: naming a file
// about to be written is legitimate, and the caller's lexical check governs it.
func Resolve(path string) (string, error) {
	if real, err := filepath.EvalSymlinks(path); err == nil && !IsLink(real) {
		return real, nil
	}

	cur := filepath.Clean(path)
	for hop := 0; ; hop++ {
		if hop > maxHops {
			return "", fmt.Errorf("too many links resolving %q", path)
		}
		next, changed, err := followFirstLink(cur)
		if err != nil {
			return "", err
		}
		if !changed {
			return cur, nil
		}
		cur = next
	}
}

// followFirstLink rewrites the shallowest component of path that is a link,
// reporting whether it found one.
func followFirstLink(path string) (string, bool, error) {
	vol := filepath.VolumeName(path)
	rest := strings.Trim(strings.TrimPrefix(path, vol), string(filepath.Separator))
	if rest == "" {
		return path, false, nil
	}
	parts := strings.Split(rest, string(filepath.Separator))

	cur := vol + string(filepath.Separator)
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				// Nothing is there, so there is nothing to smuggle.
				return path, false, nil
			}
			return "", false, err
		}
		// A junction reports neither ModeSymlink nor ModeDir, only
		// ModeIrregular, so ask Readlink rather than trust the mode bits.
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
			continue
		}
		target, err := os.Readlink(cur)
		if err != nil {
			continue // irregular but not a link: a device, a socket
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(cur), target)
		}
		return filepath.Join(append([]string{target}, parts[i+1:]...)...), true, nil
	}
	return path, false, nil
}

// IsLink reports whether path is itself a link of any kind, junctions included.
func IsLink(path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
		return false
	}
	_, err = os.Readlink(path)
	return err == nil
}
