package api

import (
	"strings"

	"github.com/xibodev/compa/v3/pkg/config"
)

// redactedProxyPassword replaces the password of a proxy URL in every API
// response. Sending the redacted URL back unchanged keeps the stored proxy.
const redactedProxyPassword = "****"

// redactProxyURL returns raw with the password of its userinfo replaced by
// redactedProxyPassword. URLs without a password come back unchanged.
func redactProxyURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	schemeEnd := strings.Index(trimmed, "://")
	if schemeEnd < 0 {
		return raw
	}
	authorityStart := schemeEnd + 3
	authority := trimmed[authorityStart:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return raw
	}
	user, _, hasPassword := strings.Cut(authority[:at], ":")
	if !hasPassword {
		return raw
	}
	return trimmed[:authorityStart] + user + ":" + redactedProxyPassword + trimmed[authorityStart+at:]
}

// redactProviderInstanceRuntime returns a copy of runtime safe to show.
func redactProviderInstanceRuntime(runtime *config.ProviderInstanceRuntime) *config.ProviderInstanceRuntime {
	clone := cloneProviderInstanceRuntime(runtime)
	if clone != nil {
		clone.Proxy = redactProxyURL(clone.Proxy)
	}
	return clone
}

// keepRedactedProxy restores the stored proxy when incoming carries exactly the
// redacted form of it, so a round-tripped form keeps the secret. Any other
// value, including a new password, replaces the stored proxy.
func keepRedactedProxy(incoming, stored *config.ProviderInstanceRuntime) {
	if incoming == nil || stored == nil {
		return
	}
	proxy := strings.TrimSpace(incoming.Proxy)
	storedProxy := strings.TrimSpace(stored.Proxy)
	if proxy == "" || storedProxy == "" {
		return
	}
	if redacted := redactProxyURL(storedProxy); redacted != storedProxy && proxy == redacted {
		incoming.Proxy = storedProxy
	}
}

// keepRedactedInstanceProxies applies keepRedactedProxy to every incoming
// instance that already exists in stored, matched by ID.
func keepRedactedInstanceProxies(incoming, stored []*config.ProviderInstanceConfig) {
	byID := make(map[string]*config.ProviderInstanceConfig, len(stored))
	for _, instance := range stored {
		if instance != nil {
			byID[instance.ID] = instance
		}
	}
	for _, instance := range incoming {
		if instance == nil {
			continue
		}
		if previous := byID[instance.ID]; previous != nil {
			keepRedactedProxy(instance.Runtime, previous.Runtime)
		}
	}
}

// redactInstanceProxies redacts the proxy password of every instance in place.
func redactInstanceProxies(instances []*config.ProviderInstanceConfig) {
	for _, instance := range instances {
		if instance != nil && instance.Runtime != nil {
			instance.Runtime.Proxy = redactProxyURL(instance.Runtime.Proxy)
		}
	}
}
