package modelservice

import (
	"context"
	"errors"
	"slices"
	"strings"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/extension"
	"github.com/xibodev/llmgw-core/translation"

	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/providers"
)

// NewExtensionClient returns the client of the daemon at endpoint using secret.
// The secret only goes to a daemon providers.CheckExtensionEndpoint accepts.
func NewExtensionClient(endpoint, secret string) (*extension.Client, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return nil, errors.New("extension daemon URL is required")
	}
	if err := providers.CheckExtensionEndpoint(endpoint, secret); err != nil {
		return nil, err
	}
	return extension.NewClient(extension.Config{BaseURL: endpoint, Secret: secret, HTTPClient: providers.ExtensionControlClient()})
}

// DiscoverExtension returns the providers the daemon at endpoint serves.
func DiscoverExtension(ctx context.Context, endpoint, secret string) (extension.InfoResponse, error) {
	client, err := NewExtensionClient(endpoint, secret)
	if err != nil {
		return extension.InfoResponse{}, err
	}
	return client.Info(ctx)
}

// ExtensionSurfaces returns the llmgw-core surfaces a daemon provider lists,
// lower-cased, de-duplicated and in the daemon's order; surfaces core does not
// define are left out.
func ExtensionSurfaces(info extension.ProviderInfo) []string {
	surfaces := make([]string, 0, len(info.Surfaces))
	for _, surface := range info.Surfaces {
		name := core.ModelSurface(strings.ToLower(strings.TrimSpace(string(surface))))
		if core.KnownModelSurface(name) && !slices.Contains(surfaces, string(name)) {
			surfaces = append(surfaces, string(name))
		}
	}
	return surfaces
}

// ExtensionSurface returns the primary surface of a daemon provider, recorded
// as its instance's protocol, or "" when it lists no surface core defines.
// Chat Completions is preferred, since Compa's OpenAI-compatible provider
// streams, then Messages; a provider serving neither, such as a speech-only
// one, is recorded under the first surface it lists.
func ExtensionSurface(info extension.ProviderInfo) string {
	surfaces := ExtensionSurfaces(info)
	for _, preferred := range []string{config.ExtensionSurfaceChatCompletions, config.ExtensionSurfaceMessages} {
		if slices.Contains(surfaces, preferred) {
			return preferred
		}
	}
	if len(surfaces) > 0 {
		return surfaces[0]
	}
	return ""
}

// ServesChat reports whether a catalog model with surfaces can be selected
// for chat: Compa's core provider serves Chat Completions natively or through
// translation.Adapter, as translation.ServesChat reports. A catalog that
// reports no surfaces leaves them unknown, so such a model stays selectable.
func ServesChat(surfaces []string) bool {
	if len(surfaces) == 0 {
		return true
	}
	native := make([]core.ModelSurface, 0, len(surfaces))
	for _, surface := range surfaces {
		native = append(native, core.ModelSurface(surface))
	}
	return translation.ServesChat(native...)
}
