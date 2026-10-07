package auth

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
)

// Provider keys and sign-in tokens live in the auth store rather than the
// config, so the config's sensitive-data filter would not know them. The
// store registers them as a source the filter consults on every call.
func init() {
	config.RegisterSensitiveValuesSource(storedSecrets)
}

// loadSecretsStore reads the store storedSecrets lists. Tests replace it.
var loadSecretsStore = loadStoreAt

// secretsCache holds the secrets last read from an auth file, keyed by the
// file's path, modification time and size.
type secretsCache struct {
	mu      sync.Mutex
	path    string
	modTime time.Time
	size    int64
	values  []string
}

var storedSecretsCache secretsCache

// storedSecrets returns every non-empty access, refresh and ID token in the
// auth store, sorted and without duplicates. It rereads auth.json only when
// the file changes, so the filter can consult it on every call; a store
// that cannot be read keeps the secrets last read from it.
func storedSecrets() []string {
	return storedSecretsCache.secrets(authFilePath())
}

func (c *secretsCache) secrets(path string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		c.path, c.modTime, c.size, c.values = path, time.Time{}, 0, nil
		return nil
	}
	if err != nil {
		return c.valuesFor(path)
	}
	if c.path == path && c.modTime.Equal(info.ModTime()) && c.size == info.Size() {
		return c.values
	}
	store, err := loadSecretsStore(path)
	if err != nil {
		return c.valuesFor(path)
	}
	c.path, c.modTime, c.size = path, info.ModTime(), info.Size()
	c.values = credentialSecrets(store)
	return c.values
}

// valuesFor returns the cached secrets when they were read from path.
func (c *secretsCache) valuesFor(path string) []string {
	if c.path != path {
		return nil
	}
	return c.values
}

func credentialSecrets(store *AuthStore) []string {
	var values []string
	for _, cred := range store.Credentials {
		if cred == nil {
			continue
		}
		for _, token := range []string{cred.AccessToken, cred.RefreshToken, cred.IDToken} {
			if token = strings.TrimSpace(token); token != "" {
				values = append(values, token)
			}
		}
	}
	slices.Sort(values)
	return slices.Compact(values)
}
