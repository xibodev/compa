package api

import (
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/modelservice"
)

// CatalogModel is one model in a provider instance's saved catalog.
type CatalogModel = modelservice.CatalogModel

// CatalogEntry is the saved catalog of one provider instance, keyed by the
// instance's ID.
type CatalogEntry = modelservice.CatalogEntry

// CatalogStore holds the saved catalogs of the provider instances.
type CatalogStore = modelservice.CatalogStore

func saveProviderInstanceCatalog(instance *config.ProviderInstanceConfig, models []CatalogModel) error {
	return modelservice.SaveProviderInstanceCatalog(instance, models)
}

func deleteProviderInstanceCatalog(instanceID string) error {
	return modelservice.DeleteProviderInstanceCatalog(instanceID)
}

func loadCatalogs() (*CatalogStore, error) {
	return modelservice.LoadCatalogs()
}

func saveCatalogs(store *CatalogStore) error {
	return modelservice.SaveCatalogs(store)
}
