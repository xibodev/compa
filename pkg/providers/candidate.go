package providers

import "strings"

// FallbackCandidate is one target a request may run on: an instance's model,
// in the order a route lists its targets.
type FallbackCandidate struct {
	Provider    string
	Model       string
	DisplayName string // selection label shown and persisted, such as "instance-id/model-id"
	RPM         int    // requests per minute; 0 means unrestricted
	IdentityKey string // optional stable identity, such as the owning instance
	ConfigKey   string // optional stable configuration identity, such as the exact target
}

// StableKey returns the candidate's config-level identity when available,
// otherwise it falls back to the runtime provider/model key. It keys the
// provider built for the candidate.
func (c FallbackCandidate) StableKey() string {
	if configKey := strings.TrimSpace(c.ConfigKey); configKey != "" {
		if identityKey := strings.TrimSpace(c.IdentityKey); identityKey != "" {
			return identityKey + "|" + configKey
		}
		return configKey
	}
	if key := strings.TrimSpace(c.IdentityKey); key != "" {
		return key
	}
	return ModelKey(c.Provider, c.Model)
}

// InstanceKey returns the key of the instance the candidate runs on. It keys
// the instance's health, so a circuit or cooldown holds back every target of
// that instance, and its RPM limit, which the instance's runtime settings
// set. A candidate without an instance identity falls back to its stable key.
func (c FallbackCandidate) InstanceKey() string {
	if key := strings.TrimSpace(c.IdentityKey); key != "" {
		return key
	}
	return c.StableKey()
}
