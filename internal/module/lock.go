package module

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// moduleLocks serialises changes to one module inside this process. The file
// lock taken alongside does the same across processes, since the CLI and the
// dashboard can both install or remove a module.
var moduleLocks sync.Map // lock path -> *sync.Mutex

// lockModule takes the per-module install lock and returns its release.
func lockModule(home, id string) (func(), error) {
	dir, err := filepath.Abs(filepath.Join(ModulesDir(home), ".locks"))
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".lock")

	value, _ := moduleLocks.LoadOrStore(path, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		mu.Unlock()
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		mu.Unlock()
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		mu.Unlock()
		return nil, fmt.Errorf("lock module %s: %w", id, err)
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
		mu.Unlock()
	}, nil
}
