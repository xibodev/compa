package providers

import (
	"strings"

	coreproviders "github.com/xibodev/llmgw-core/providers"
)

// NormalizeProvider returns the canonical llmgw-core registry id of provider,
// resolving the registry's aliases. An identifier the registry does not know
// comes back lower-cased and trimmed.
func NormalizeProvider(provider string) string {
	if strings.TrimSpace(provider) == "" {
		return ""
	}
	return coreproviders.CanonicalRegistryID(provider)
}

// ModelKey returns a canonical "provider/model" key for deduplication.
func ModelKey(provider, model string) string {
	return NormalizeProvider(provider) + "/" + strings.ToLower(strings.TrimSpace(model))
}
