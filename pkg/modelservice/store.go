package modelservice

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/fileutil"
)

var catalogStoreMu sync.RWMutex

// CatalogFilePath returns the absolute path to the local model catalogs cache file.
func CatalogFilePath() string {
	return filepath.Join(config.GetHome(), "model_catalogs.json")
}

// LoadCatalogs reads the model catalogs cache from disk.
func LoadCatalogs() (*CatalogStore, error) {
	catalogStoreMu.RLock()
	defer catalogStoreMu.RUnlock()
	return loadCatalogsUnlocked()
}

func loadCatalogsUnlocked() (*CatalogStore, error) {
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
		return &CatalogStore{Entries: make(map[string]*CatalogEntry)}, nil
	}
	if store.Entries == nil {
		store.Entries = make(map[string]*CatalogEntry)
	}
	return &store, nil
}

// SaveCatalogs atomically writes the model catalogs cache to disk.
func SaveCatalogs(store *CatalogStore) error {
	catalogStoreMu.Lock()
	defer catalogStoreMu.Unlock()
	return saveCatalogsUnlocked(store)
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

// SaveProviderInstanceCatalog saves catalog models for a specific instance.
func SaveProviderInstanceCatalog(instance *config.ProviderInstanceConfig, models []CatalogModel) error {
	catalogStoreMu.Lock()
	defer catalogStoreMu.Unlock()
	store, err := loadCatalogsUnlocked()
	if err != nil {
		return err
	}
	store.Entries[instance.ID] = &CatalogEntry{
		ID:         instance.ID,
		InstanceID: instance.ID,
		Provider:   instance.ProviderKind,
		APIBase:    strings.TrimRight(strings.TrimSpace(instance.Endpoint), "/"),
		Models:     models,
		FetchedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	return saveCatalogsUnlocked(store)
}

// DeleteProviderInstanceCatalog removes catalog models for a specific instance.
func DeleteProviderInstanceCatalog(instanceID string) error {
	catalogStoreMu.Lock()
	defer catalogStoreMu.Unlock()
	store, err := loadCatalogsUnlocked()
	if err != nil {
		return err
	}
	delete(store.Entries, instanceID)
	return saveCatalogsUnlocked(store)
}
