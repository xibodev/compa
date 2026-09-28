package auth

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/fileutil"
)

type AuthCredential struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	AccountID    string    `json:"account_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	Provider     string    `json:"provider"`
	AuthMethod   string    `json:"auth_method"`
	// IDToken, TokenType and Metadata round-trip a signed-in credential's
	// tokenstore.Record. Metadata may hold secrets; it is stored like tokens.
	IDToken   string            `json:"id_token,omitempty"`
	TokenType string            `json:"token_type,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	// Revision changes on every write through the token store, which
	// compares it to fence out writers whose view is stale.
	Revision string `json:"revision,omitempty"`
}

type AuthStore struct {
	Credentials map[string]*AuthCredential `json:"credentials"`
}

var authStoreMu sync.Mutex

// ExtensionDaemonKey stores the extension daemon's shared secret.
const ExtensionDaemonKey = "extension-daemon"

// withAuthFileLock runs fn while holding an OS lock that every process
// sharing this home honours, so read-modify-write updates of auth.json never
// interleave. The lock lives on a sidecar file because auth.json is replaced
// by rename, which would drop a lock held on it. The OS releases the lock if
// the holder dies, so there is no staleness heuristic. Callers hold
// authStoreMu first, since some platforms grant the OS lock per process.
func withAuthFileLock(fn func() error) error {
	return withAuthFileLockAt(authFilePath(), fn)
}

func withAuthFileLockAt(path string, fn func() error) error {
	lockPath := path + ".flock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(lockPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("opening auth store lock: %w", err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return fmt.Errorf("locking auth store: %w", err)
	}
	defer func() { _ = unlockFile(f) }()
	return fn()
}

func (c *AuthCredential) IsExpired() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().After(c.ExpiresAt)
}

func (c *AuthCredential) NeedsRefresh() bool {
	if c.ExpiresAt.IsZero() {
		return false
	}
	return time.Now().Add(5 * time.Minute).After(c.ExpiresAt)
}

func authFilePath() string {
	return filepath.Join(config.GetHome(), "auth.json")
}

func canonicalProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func cloneCredential(cred *AuthCredential) *AuthCredential {
	if cred == nil {
		return nil
	}
	cp := *cred
	cp.Metadata = maps.Clone(cred.Metadata)
	return &cp
}

func LoadStore() (*AuthStore, error) {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	return loadStoreUnlocked()
}

func loadStoreUnlocked() (*AuthStore, error) {
	return loadStoreAt(authFilePath())
}

func loadStoreAt(path string) (*AuthStore, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AuthStore{Credentials: make(map[string]*AuthCredential)}, nil
		}
		return nil, err
	}

	var store AuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, err
	}
	if store.Credentials == nil {
		store.Credentials = make(map[string]*AuthCredential)
	}
	return &store, nil
}

func saveStoreUnlocked(store *AuthStore) error {
	return saveStoreAt(authFilePath(), store)
}

func saveStoreAt(path string, store *AuthStore) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}

	// Use unified atomic write utility with explicit sync for flash storage reliability.
	return fileutil.WriteFileAtomic(path, data, 0o600)
}

func GetCredential(provider string) (*AuthCredential, error) {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	return getCredentialUnlocked(provider)
}

func getCredentialUnlocked(provider string) (*AuthCredential, error) {
	store, err := loadStoreUnlocked()
	if err != nil {
		return nil, err
	}
	cred, ok := store.Credentials[canonicalProvider(provider)]
	if !ok {
		return nil, nil
	}
	return cred, nil
}

func SetCredential(provider string, cred *AuthCredential) error {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	return withAuthFileLock(func() error { return setCredentialUnlocked(provider, cred) })
}

func setCredentialUnlocked(provider string, cred *AuthCredential) error {
	store, err := loadStoreUnlocked()
	if err != nil {
		return err
	}

	canonical := canonicalProvider(provider)
	normalized := cloneCredential(cred)
	if normalized != nil {
		normalized.Provider = canonicalProvider(normalized.Provider)
		if normalized.Provider == "" {
			normalized.Provider = canonical
		}
	}

	store.Credentials[canonical] = normalized
	return saveStoreUnlocked(store)
}

func DeleteCredential(provider string) error {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	return withAuthFileLock(func() error { return deleteCredentialUnlocked(provider) })
}

func DeleteCredentials(providers ...string) error {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	return withAuthFileLock(func() error {
		store, err := loadStoreUnlocked()
		if err != nil {
			return err
		}
		for _, provider := range providers {
			delete(store.Credentials, canonicalProvider(provider))
		}
		return saveStoreUnlocked(store)
	})
}

func deleteCredentialUnlocked(provider string) error {
	store, err := loadStoreUnlocked()
	if err != nil {
		return err
	}
	delete(store.Credentials, canonicalProvider(provider))
	return saveStoreUnlocked(store)
}

func DeleteAllCredentials() error {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	return withAuthFileLock(func() error {
		path := authFilePath()
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	})
}
