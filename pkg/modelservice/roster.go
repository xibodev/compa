package modelservice

import (
	"slices"
	"sort"
	"strings"

	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/providers"
	llmgwproviders "github.com/xibodev/llmgw-core/providers"
)

// rosterDuplicates maps a core registry entry the roster leaves out to the
// entry that offers the same provider in its place; instances of the left-out
// kind still run and count as the kept entry's.
//
// Core's registry lists Google's Gemini API twice, under the same API key at
// generativelanguage.googleapis.com: "gemini", its OpenAI-compatible
// endpoint, and "ai_studio", its native generateContent API. Compa keeps
// "gemini": Compa's chat client speaks Chat Completions to it natively,
// streaming and calling tools, while core's native Google provider serves
// chat without streaming and drops tools.
var rosterDuplicates = map[string]string{"ai_studio": "gemini"}

// ListRoster returns the providers Compa offers: every llmgw-core registry
// entry Compa serves, with the instances configured for it, once per
// provider (see rosterDuplicates). Core's registry is the only source of
// provider identity (label, description, default endpoint, auth); Compa keeps
// no provider presets of its own.
func ListRoster(cfg *config.Config) []ProviderRosterItem {
	configuredByKind := make(map[string]map[string]struct{})
	addConfigured := func(key, instanceID string) {
		key = strings.TrimSpace(key)
		if key == "" || instanceID == "" {
			return
		}
		if configuredByKind[key] == nil {
			configuredByKind[key] = make(map[string]struct{})
		}
		configuredByKind[key][instanceID] = struct{}{}
	}
	if cfg != nil {
		for _, inst := range cfg.ProviderInstances {
			if inst == nil {
				continue
			}
			normKind := providers.NormalizeProvider(inst.ProviderKind)
			addConfigured(normKind, inst.ID)
			addConfigured(inst.ProviderKind, inst.ID)
			addConfigured(inst.ID, inst.ID)
		}
	}

	registry := llmgwproviders.ProviderRegistry()
	servable := make(map[string]bool, len(registry))
	for _, reg := range registry {
		servable[reg.ID] = rosterServable(reg)
	}
	rosterMap := make(map[string]ProviderRosterItem)

	for _, reg := range registry {
		if !servable[reg.ID] {
			continue
		}
		if kept := rosterDuplicates[reg.ID]; kept != "" && servable[kept] {
			continue
		}
		entry := ProviderRosterItem{
			ID:                  reg.ID,
			DisplayName:         reg.Label,
			Label:               reg.Label,
			Description:         reg.Description,
			Categories:          append([]string(nil), reg.Categories...),
			DefaultEndpoint:     reg.DefaultBaseURL,
			RequiresAPIKey:      reg.RequiresAPIKey,
			RequiresBaseURL:     reg.RequiresBaseURL,
			AnonymousAutomation: reg.AnonymousAutomation,
			AuthMethods:         append([]string(nil), reg.AuthMethods...),
			OnboardingFields:    append([]string(nil), reg.OnboardingFields...),
			Compatibility:       "discovery_only",
		}

		if reg.Protocol != "" {
			entry.Protocol = reg.Protocol
		} else {
			entry.Protocol = reg.RuntimeType
		}

		normID := providers.NormalizeProvider(reg.ID)
		if adapter, ok := providers.RegistryAdapter(reg); ok {
			entry.Adapter = adapter
			entry.Compatibility = "compatible"
		}

		instSet := make(map[string]struct{})
		kinds := []string{reg.ID}
		if len(configuredByKind[reg.ID]) == 0 {
			kinds = []string{normID}
		}
		for duplicate, kept := range rosterDuplicates {
			if kept == reg.ID {
				kinds = append(kinds, duplicate)
			}
		}
		for _, kind := range kinds {
			for id := range configuredByKind[kind] {
				instSet[id] = struct{}{}
			}
		}
		if len(instSet) > 0 {
			insts := make([]string, 0, len(instSet))
			for id := range instSet {
				insts = append(insts, id)
			}
			sort.Strings(insts)
			entry.Configured = true
			entry.InstanceCount = len(insts)
			entry.ConfiguredInstances = insts
		}

		rosterMap[reg.ID] = entry
	}

	roster := make([]ProviderRosterItem, 0, len(rosterMap))
	for _, entry := range rosterMap {
		roster = append(roster, entry)
	}

	sort.Slice(roster, func(i, j int) bool { return roster[i].ID < roster[j].ID })
	return roster
}

// rosterServable reports whether Compa serves a registry entry itself: one
// available to products, whose sign-in is an API key or none rather than a
// provider-specific flow, and whose runtime Compa runs on llmgw-core.
func rosterServable(reg llmgwproviders.RegistryEntry) bool {
	if reg.ClientOnly || strings.TrimSpace(reg.AuthAdapter) != "" || !strings.EqualFold(reg.Availability, "available") {
		return false
	}
	if !slices.ContainsFunc(reg.AuthMethods, func(method string) bool { return method == "api_key" || method == "none" }) {
		return false
	}
	_, ok := providers.RegistryAdapter(reg)
	return ok
}
