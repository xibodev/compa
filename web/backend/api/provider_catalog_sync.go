package api

import (
	"context"
	"fmt"

	"github.com/xibodev/compa/pkg/modelservice"
)

// syncProviderCatalog lists an instance's models through its llmgw-core
// provider.
func (h *Handler) syncProviderCatalog(ctx context.Context, input ProviderCatalogSyncInput) ([]CatalogModel, error) {
	if !modelservice.CatalogSyncSupported(input.Instance()) {
		return nil, fmt.Errorf("catalog discovery is not supported for adapter %q", input.Adapter)
	}
	return modelservice.SyncCatalog(ctx, input)
}
