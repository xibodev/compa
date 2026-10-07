package modelservice

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
)

// A corrupt catalogs file is never read as empty and then overwritten: it is
// moved aside with its content intact, the error says where, and the next
// read starts fresh.
func TestCorruptCatalogsAreMovedAsideAndReported(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	path := CatalogFilePath()
	const corrupt = `{"entries": {"openai": {"id": "openai"` // truncated mid-write
	if err := os.WriteFile(path, []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}

	instance := &config.ProviderInstanceConfig{ID: "anthropic", ProviderKind: "anthropic"}
	err := SaveProviderInstanceCatalog(instance, []CatalogModel{{ID: "claude"}})
	if err == nil || !strings.Contains(err.Error(), "corrupt") || !strings.Contains(err.Error(), ".corrupt-") {
		t.Fatalf("SaveProviderInstanceCatalog() error = %v, want the corrupt file reported with its backup", err)
	}
	backups, _ := filepath.Glob(path + ".corrupt-*")
	if len(backups) != 1 {
		t.Fatalf("backups = %v, want one", backups)
	}
	if got, _ := os.ReadFile(backups[0]); string(got) != corrupt {
		t.Fatalf("backup holds %q, want the corrupt content", got)
	}

	store, err := LoadCatalogs()
	if err != nil || len(store.Entries) != 0 {
		t.Fatalf("LoadCatalogs() after the move = %v, %v; want an empty store", store, err)
	}
	if err := SaveProviderInstanceCatalog(instance, []CatalogModel{{ID: "claude"}}); err != nil {
		t.Fatalf("SaveProviderInstanceCatalog() after the move error = %v", err)
	}
	if store, err := LoadCatalogs(); err != nil || store.Entries["anthropic"] == nil {
		t.Fatalf("LoadCatalogs() = %v, %v; want the saved catalog", store, err)
	}
}

// A plain read reports the corrupt file too, rather than returning nothing.
func TestLoadCatalogsReportsCorruptFile(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := os.WriteFile(CatalogFilePath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalogs(); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("LoadCatalogs() error = %v, want the corrupt file reported", err)
	}
	if _, err := os.Stat(CatalogFilePath()); !os.IsNotExist(err) {
		t.Fatalf("the corrupt file is still in place: %v", err)
	}
}
