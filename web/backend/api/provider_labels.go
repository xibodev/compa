package api

// What the dashboard shows of a provider instance beyond its configuration:
// the name to show for it, what manages it, and the credential it needs.

import (
	"context"
	"strings"

	coreproviders "github.com/xibodev/llmgw-core/providers"

	"github.com/xibodev/compa/v4/pkg/config"
)

// Credential kinds a provider instance reports: what it signs in with.
const (
	credentialKindNone   = "none"
	credentialKindAPIKey = "api_key"
	credentialKindToken  = "token"
	credentialKindOAuth  = "oauth"
)

// managedByExtension is the manager of an instance that mirrors a provider
// the extension daemon serves: connecting the daemon creates and updates it.
const managedByExtension = "extension"

// providerInstanceDisplayName is the name to show for an instance: an
// extension instance's provider as the daemon names it, the core registry's
// label of a registry provider kind, else the instance's id.
func providerInstanceDisplayName(instance *config.ProviderInstanceConfig) string {
	if instance == nil {
		return ""
	}
	if instance.ExtensionProvider() != "" {
		if name, _ := instance.Settings[config.ExtensionDisplayNameSetting].(string); strings.TrimSpace(name) != "" {
			return strings.TrimSpace(name)
		}
		return instance.ID
	}
	if entry, ok := coreproviders.RegistryProvider(strings.TrimSpace(instance.ProviderKind)); ok && strings.TrimSpace(entry.Label) != "" {
		return strings.TrimSpace(entry.Label)
	}
	return instance.ID
}

// providerInstanceManagedBy is what manages an instance: "extension" for one
// that mirrors a daemon provider, "" for one the owner configured.
func providerInstanceManagedBy(instance *config.ProviderInstanceConfig) string {
	if instance.ExtensionProvider() != "" {
		return managedByExtension
	}
	return ""
}

// providerInstanceCredential returns the kind of credential an instance
// signs in with and whether it is ready: stored, signed in, or not needed.
//
// An extension instance needs what its daemon provider declares: nothing, a
// pasted token, or a sign-in, whose record the token store holds. Any other
// instance needs an API key when its registry provider requires one, or has
// one configured; otherwise it needs none. A stored credential counts only
// when it resolves.
func (h *Handler) providerInstanceCredential(instance *config.ProviderInstanceConfig) (string, bool) {
	if instance.ExtensionProvider() != "" {
		kind, _ := instance.Settings[config.ExtensionCredentialSetting].(string)
		switch strings.ToLower(strings.TrimSpace(kind)) {
		case extensionCredentialToken:
			return credentialKindToken, h.credentialResolves(instance.AuthConnectionRef)
		case extensionCredentialOAuth:
			key, _ := instance.Settings[config.ExtensionCredentialKeySetting].(string)
			if key = strings.TrimSpace(key); key == "" {
				return credentialKindOAuth, false
			}
			_, err := extensionTokenStore().Load(context.Background(), key)
			return credentialKindOAuth, err == nil
		}
		return credentialKindNone, true
	}
	required := false
	if entry, ok := coreproviders.RegistryProvider(strings.TrimSpace(instance.ProviderKind)); ok {
		required = entry.RequiresAPIKey
	}
	if !required && strings.TrimSpace(instance.AuthConnectionRef) == "" {
		return credentialKindNone, true
	}
	return credentialKindAPIKey, h.credentialResolves(instance.AuthConnectionRef)
}

// credentialResolves reports whether an auth_connection_ref resolves to a
// secret. The secret itself is discarded.
func (h *Handler) credentialResolves(ref string) bool {
	if strings.TrimSpace(ref) == "" || h.providerCredentialResolver == nil {
		return false
	}
	secret, err := h.providerCredentialResolver(ref)
	return err == nil && strings.TrimSpace(secret) != ""
}
