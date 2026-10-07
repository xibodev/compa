package modelservice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/fileutil"
)

// catalogStoreMu serializes this process; config.WithFileLock serializes the
// processes (CLI, launcher, kernel) that write the file.
var catalogStoreMu sync.RWMutex

// CatalogFilePath returns the absolute path to the local model catalogs cache file.
func CatalogFilePath() string {
	return filepath.Join(config.GetHome(), "model_catalogs.json")
}

// LoadCatalogs reads the model catalogs cache from disk.
func LoadCatalogs() (*CatalogStore, error) {
	catalogStoreMu.RLock()
	defer catalogStoreMu.RUnlock()
	return loadCatalogsUnlocked(false)
}

// loadCatalogsUnlocked reads the cache. A file that does not parse is never
// taken as empty, which the next save would turn into lost catalogs: it is
// moved aside to model_catalogs.json.corrupt-<time> and reported, so the
// next read starts fresh and the content stays recoverable. holdingFileLock
// says whether the caller already holds the cross-process lock.
func loadCatalogsUnlocked(holdingFileLock bool) (*CatalogStore, error) {
	path := CatalogFilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &CatalogStore{Entries: make(map[string]*CatalogEntry)}, nil
		}
		return nil, fmt.Errorf("read model catalogs: %w", err)
	}
	var store CatalogStore
	if err := json.Unmarshal(data, &store); err != nil {
		parseErr := fmt.Errorf("model catalogs file %s is corrupt: %w", path, err)
		backup, moveErr := moveCorruptCatalogs(path, data, holdingFileLock)
		switch {
		case moveErr != nil:
			return nil, fmt.Errorf("%w; moving it aside failed: %v", parseErr, moveErr)
		case backup != "":
			return nil, fmt.Errorf("%w; it was moved to %s and the catalogs start empty", parseErr, backup)
		}
		return nil, parseErr
	}
	if store.Entries == nil {
		store.Entries = make(map[string]*CatalogEntry)
	}
	return &store, nil
}

// moveCorruptCatalogs renames the corrupt file aside, unless another writer
// replaced it since it was read. It returns the backup path, or "" when the
// file had already changed.
func moveCorruptCatalogs(path string, corrupt []byte, holdingFileLock bool) (string, error) {
	backup := ""
	move := func() error {
		current, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(current, corrupt) {
			return nil
		}
		backup = path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
		return os.Rename(path, backup)
	}
	if holdingFileLock {
		return backup, move()
	}
	return backup, config.WithFileLock(path, move)
}

// SaveCatalogs atomically writes the model catalogs cache to disk.
func SaveCatalogs(store *CatalogStore) error {
	catalogStoreMu.Lock()
	defer catalogStoreMu.Unlock()
	return config.WithFileLock(CatalogFilePath(), func() error { return saveCatalogsUnlocked(store) })
}

func saveCatalogsUnlocked(store *CatalogStore) error {
	path := CatalogFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create catalog directory: %w", err)
	}
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal catalog: %w", err)
	}
	return fileutil.WriteFileAtomic(path, data, 0600)
}

// updateCatalogs reads, changes and writes the cache under both locks, so a
// concurrent writer in another process is not overwritten.
func updateCatalogs(change func(*CatalogStore)) error {
	catalogStoreMu.Lock()
	defer catalogStoreMu.Unlock()
	return config.WithFileLock(CatalogFilePath(), func() error {
		store, err := loadCatalogsUnlocked(true)
		if err != nil {
			return err
		}
		change(store)
		return saveCatalogsUnlocked(store)
	})
}

// SaveProviderInstanceCatalog saves catalog models for a specific instance.
func SaveProviderInstanceCatalog(instance *config.ProviderInstanceConfig, models []CatalogModel) error {
	return updateCatalogs(func(store *CatalogStore) {
		store.Entries[instance.ID] = &CatalogEntry{
			ID:         instance.ID,
			InstanceID: instance.ID,
			Provider:   instance.ProviderKind,
			APIBase:    strings.TrimRight(strings.TrimSpace(instance.Endpoint), "/"),
			Models:     models,
			FetchedAt:  time.Now().UTC().Format(time.RFC3339),
		}
	})
}

// DeleteProviderInstanceCatalog removes catalog models for a specific instance.
func DeleteProviderInstanceCatalog(instanceID string) error {
	return updateCatalogs(func(store *CatalogStore) {
		delete(store.Entries, instanceID)
	})
}
