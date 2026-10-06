package modelservice

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/xibodev/compa/v2/pkg/config"
)

// Ping probes an instance's reachability and measures round-trip latency.
func Ping(ctx context.Context, instance *config.ProviderInstanceConfig, secret string, client *http.Client) PingResult {
	return PingWithSync(ctx, instance, secret, client, nil)
}

// PingWithSync probes an instance by listing its catalog with syncFunc, nil
// using SyncCatalog; an instance without a catalog is probed with a GET of
// its endpoint over client.
func PingWithSync(ctx context.Context, instance *config.ProviderInstanceConfig, secret string, client *http.Client, syncFunc CatalogSyncFunc) PingResult {
	if instance == nil {
		return PingResult{
			OK:     false,
			Status: "unreachable",
			Error:  "nil provider instance",
		}
	}

	start := time.Now()

	if CatalogSyncSupported(instance) {
		input := CatalogSyncInputFromInstance(instance)
		input.Secret = secret
		if syncFunc == nil {
			syncFunc = SyncCatalog
		}
		models, syncErr := syncFunc(ctx, input)
		latencyMs := time.Since(start).Milliseconds()
		if syncErr != nil {
			return PingResult{
				OK:         false,
				InstanceID: instance.ID,
				LatencyMS:  latencyMs,
				Status:     "unreachable",
				Error:      syncErr.Error(),
			}
		}
		return PingResult{
			OK:         true,
			InstanceID: instance.ID,
			LatencyMS:  latencyMs,
			ModelCount: len(models),
			Status:     "reachable",
		}
	}

	if strings.TrimSpace(instance.Endpoint) != "" {
		if client == nil {
			client = http.DefaultClient
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, instance.Endpoint, nil)
		if reqErr != nil {
			return PingResult{
				OK:         false,
				InstanceID: instance.ID,
				LatencyMS:  0,
				Status:     "unreachable",
				Error:      reqErr.Error(),
			}
		}
		resp, respErr := client.Do(req)
		latencyMs := time.Since(start).Milliseconds()
		if respErr != nil {
			return PingResult{
				OK:         false,
				InstanceID: instance.ID,
				LatencyMS:  latencyMs,
				Status:     "unreachable",
				Error:      respErr.Error(),
			}
		}
		_ = resp.Body.Close()
		return PingResult{
			OK:         true,
			InstanceID: instance.ID,
			LatencyMS:  latencyMs,
			Status:     "reachable",
		}
	}

	return PingResult{
		OK:         true,
		InstanceID: instance.ID,
		LatencyMS:  time.Since(start).Milliseconds(),
		Status:     "configured",
	}
}
