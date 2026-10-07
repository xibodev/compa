package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/llm-provider-auth/tokenstore"
)

// TokenStore is a tokenstore.Store over an auth.json file, shared by every
// process that opens the same file: the launcher, the kernel and the CLI.
//
// Writes hold the same OS lock as every other auth.json update and replace a
// credential only when its revision still matches. A lease is an OS lock on
// a per-key sidecar file, which the OS releases if its holder dies, so a
// refresh in one process fences out the others without a staleness guess.
type TokenStore struct {
	// path is the auth.json file; empty follows the Compa home at call
	// time.
	path string
}

var _ tokenstore.Store = (*TokenStore)(nil)

// OpenTokenStore returns the store over the auth.json file at path.
func OpenTokenStore(path string) *TokenStore {
	return &TokenStore{path: path}
}

// DefaultTokenStore returns the store over the Compa home's auth.json.
func DefaultTokenStore() *TokenStore {
	return &TokenStore{}
}

func (s *TokenStore) file() string {
	if s.path != "" {
		return s.path
	}
	return authFilePath()
}

func checkTokenRequest(ctx context.Context, key string) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("tokenstore: key is required")
	}
	return ctx.Err()
}

func recordFromCredential(cred *AuthCredential) tokenstore.Record {
	return tokenstore.Record{
		Revision:     cred.Revision,
		AccessToken:  cred.AccessToken,
		RefreshToken: cred.RefreshToken,
		IDToken:      cred.IDToken,
		TokenType:    cred.TokenType,
		Expiry:       cred.ExpiresAt,
		AccountID:    cred.AccountID,
		Metadata:     cloneCredential(cred).Metadata,
	}
}

func credentialFromRecord(key string, record tokenstore.Record) *AuthCredential {
	record = record.Clone()
	return &AuthCredential{
		AccessToken:  record.AccessToken,
		RefreshToken: record.RefreshToken,
		IDToken:      record.IDToken,
		TokenType:    record.TokenType,
		ExpiresAt:    record.Expiry,
		AccountID:    record.AccountID,
		Metadata:     record.Metadata,
		Provider:     key,
		AuthMethod:   "oauth",
	}
}

func newRevision() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tokenstore: revision: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// update runs fn over the store under the in-process and OS locks and saves
// the store when fn reports a change.
func (s *TokenStore) update(fn func(store *AuthStore) (bool, error)) error {
	authStoreMu.Lock()
	defer authStoreMu.Unlock()
	path := s.file()
	return withAuthFileLockAt(path, func() error {
		store, err := loadStoreAt(path)
		if err != nil {
			return err
		}
		changed, err := fn(store)
		if err != nil || !changed {
			return err
		}
		return saveStoreAt(path, store)
	})
}

// Load returns the current record, or tokenstore.ErrNotFound.
func (s *TokenStore) Load(ctx context.Context, key string) (tokenstore.Record, error) {
	if err := checkTokenRequest(ctx, key); err != nil {
		return tokenstore.Record{}, err
	}
	authStoreMu.Lock()
	store, err := loadStoreAt(s.file())
	authStoreMu.Unlock()
	if err != nil {
		return tokenstore.Record{}, err
	}
	cred := store.Credentials[canonicalProvider(key)]
	if cred == nil {
		return tokenstore.Record{}, tokenstore.ErrNotFound
	}
	return recordFromCredential(cred), nil
}

func (s *TokenStore) write(key string, record tokenstore.Record, allowed func(current *AuthCredential) error) (tokenstore.Record, error) {
	revision, err := newRevision()
	if err != nil {
		return tokenstore.Record{}, err
	}
	stored := credentialFromRecord(key, record)
	stored.Revision = revision
	err = s.update(func(store *AuthStore) (bool, error) {
		if err := allowed(store.Credentials[canonicalProvider(key)]); err != nil {
			return false, err
		}
		store.Credentials[canonicalProvider(key)] = stored
		return true, nil
	})
	if err != nil {
		return tokenstore.Record{}, err
	}
	return recordFromCredential(stored), nil
}

// Save stores record unconditionally, as a new sign-in does.
func (s *TokenStore) Save(ctx context.Context, key string, record tokenstore.Record) (tokenstore.Record, error) {
	if err := checkTokenRequest(ctx, key); err != nil {
		return tokenstore.Record{}, err
	}
	return s.write(key, record, func(*AuthCredential) error { return nil })
}

// ReplaceIfCurrent stores record only if the stored revision is revision.
func (s *TokenStore) ReplaceIfCurrent(ctx context.Context, key, revision string, record tokenstore.Record) (tokenstore.Record, error) {
	if err := checkTokenRequest(ctx, key); err != nil {
		return tokenstore.Record{}, err
	}
	return s.write(key, record, func(current *AuthCredential) error {
		if current == nil || current.Revision == "" || current.Revision != revision {
			return tokenstore.ErrConflict
		}
		return nil
	})
}

// RevokeIfCurrent removes the credential only if its revision is revision.
func (s *TokenStore) RevokeIfCurrent(ctx context.Context, key, revision string) error {
	if err := checkTokenRequest(ctx, key); err != nil {
		return err
	}
	return s.update(func(store *AuthStore) (bool, error) {
		current := store.Credentials[canonicalProvider(key)]
		if current == nil {
			return false, tokenstore.ErrNotFound
		}
		if current.Revision == "" || current.Revision != revision {
			return false, tokenstore.ErrConflict
		}
		delete(store.Credentials, canonicalProvider(key))
		return true, nil
	})
}

// leaseLocks serializes leases inside one process, since some platforms
// grant OS locks per process rather than per open file.
var leaseLocks sync.Map // lease file path -> chan struct{} of capacity 1

const leasePollInterval = 20 * time.Millisecond

// Lease blocks until it holds the exclusive refresh lease for key or ctx
// ends. The lease holds until release is called or the process exits.
func (s *TokenStore) Lease(ctx context.Context, key string) (func(), error) {
	if err := checkTokenRequest(ctx, key); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(canonicalProvider(key)))
	path := s.file() + ".lease-" + hex.EncodeToString(sum[:8])

	slot, _ := leaseLocks.LoadOrStore(path, make(chan struct{}, 1))
	local := slot.(chan struct{})
	select {
	case local <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	releaseLocal := func() { <-local }

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		releaseLocal()
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		releaseLocal()
		return nil, fmt.Errorf("tokenstore: opening lease: %w", err)
	}
	for {
		locked, err := tryLockFile(f)
		if err != nil {
			_ = f.Close()
			releaseLocal()
			return nil, fmt.Errorf("tokenstore: taking lease: %w", err)
		}
		if locked {
			break
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			releaseLocal()
			return nil, ctx.Err()
		case <-time.After(leasePollInterval):
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = unlockFile(f)
			_ = f.Close()
			releaseLocal()
		})
	}, nil
}
