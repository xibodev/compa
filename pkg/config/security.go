// Compa - Ultra-lightweight personal AI agent
// License: MIT
//
// Copyright (c) 2026 Compa contributors

package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/xibodev/compa/v3/pkg/fileutil"
)

const (
	SecurityConfigFile = ".security.yml"
)

// securityPath returns the path to security.yml relative to the config file
func securityPath(configPath string) string {
	configDir := filepath.Dir(configPath)
	return filepath.Join(configDir, SecurityConfigFile)
}

// loadSecurityConfig loads the security configuration from security.yml
// and merges secure field values into the config.
func loadSecurityConfig(cfg *Config, securityPath string) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	data, err := os.ReadFile(securityPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("failed to read security config: %w", err)
	}

	// Save existing channels before unmarshal
	savedChannels := make(ChannelsConfig, len(cfg.Channels))
	for name, bc := range cfg.Channels {
		savedChannels[name] = bc
	}

	// Parse YAML into a yaml.Node tree to extract channels node
	var rootNode yaml.Node
	if err := yaml.Unmarshal(data, &rootNode); err != nil {
		return fmt.Errorf("failed to parse security config: %w", err)
	}

	// Extract the channel_list node
	var channelsNode *yaml.Node
	if len(rootNode.Content) > 0 {
		content := rootNode.Content[0].Content
		for i := 0; i < len(content); i += 2 {
			if i+1 < len(content) {
				key := content[i].Value
				if key == "channel_list" {
					channelsNode = content[i+1]
					break
				}
			}
		}
	}

	// Unmarshal non-channel fields from security.yml
	// This resolves the secrets of tools, skills, etc.
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("failed to parse security config %s: %w", securityPath, err)
	}

	// Restore channels from saved, then manually merge from security.yml
	cfg.Channels = make(ChannelsConfig)
	for name, savedBC := range savedChannels {
		cfg.Channels[name] = savedBC
	}

	// If we found a channels node in security.yml, merge it into existing channels
	if channelsNode != nil {
		if err := cfg.Channels.UnmarshalYAML(channelsNode); err != nil {
			return fmt.Errorf("failed to merge channels from security config: %w", err)
		}
	}

	// The secret maps' values fill the placeholders config.json holds.
	var secrets secretMapsFile
	if err := yaml.Unmarshal(data, &secrets); err != nil {
		return fmt.Errorf("failed to parse security config %s: %w", securityPath, err)
	}
	cfg.applySecretMaps(secrets, false)

	return nil
}

// saveSecurityConfig saves the security configuration to security.yml
func saveSecurityConfig(securityPath string, sec *Config) error {
	data, err := marshalSecurityConfig(sec, secretMapsFile{})
	if err != nil {
		return err
	}
	return fileutil.WriteFileAtomic(securityPath, data, 0o600)
}

// marshalSecurityConfig renders the .security.yml content of sec: its
// SecureString fields and the values of its secret maps, given the map
// values the file holds now (see storedSecrets).
func marshalSecurityConfig(sec *Config, stored secretMapsFile) ([]byte, error) {
	var doc yaml.Node
	if err := doc.Encode(sec); err != nil {
		return nil, fmt.Errorf("failed to marshal security config: %w", err)
	}
	if maps := sec.secretMapsForSave(stored); !maps.empty() {
		if err := appendYAMLMapping(&doc, maps); err != nil {
			return nil, fmt.Errorf("failed to marshal security config: %w", err)
		}
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("failed to marshal security config: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("failed to marshal security config: %w", err)
	}
	return buf.Bytes(), nil
}

// SensitiveDataCache holds the config's own secrets, collected once, and the
// replacer last built from them and the registered sources' values.
type SensitiveDataCache struct {
	once   sync.Once
	values []string

	mu       sync.Mutex
	built    []string
	replacer *strings.Replacer
}

// sensitiveSource is one RegisterSensitiveValuesSource registration.
type sensitiveSource struct {
	id     uint64
	values func() []string
}

var (
	sensitiveSourcesMu   sync.Mutex
	sensitiveSources     []sensitiveSource
	nextSensitiveSource  uint64
	sensitiveCacheInitMu sync.Mutex
)

// RegisterSensitiveValuesSource adds a source of secrets that live outside
// the config, such as the credentials in the auth store. FilterSensitiveData
// consults every source on each call, so a secret stored after the config
// was loaded is filtered too; a source should therefore be cheap. The
// returned function unregisters the source.
func RegisterSensitiveValuesSource(values func() []string) (unregister func()) {
	if values == nil {
		return func() {}
	}
	sensitiveSourcesMu.Lock()
	defer sensitiveSourcesMu.Unlock()
	nextSensitiveSource++
	id := nextSensitiveSource
	sensitiveSources = append(sensitiveSources, sensitiveSource{id: id, values: values})
	return func() {
		sensitiveSourcesMu.Lock()
		defer sensitiveSourcesMu.Unlock()
		sensitiveSources = slices.DeleteFunc(sensitiveSources, func(source sensitiveSource) bool {
			return source.id == id
		})
	}
}

// registeredSensitiveValues returns the current values of every registered
// source. Sources run outside the registry lock.
func registeredSensitiveValues() []string {
	sensitiveSourcesMu.Lock()
	sources := make([]func() []string, 0, len(sensitiveSources))
	for _, source := range sensitiveSources {
		sources = append(sources, source.values)
	}
	sensitiveSourcesMu.Unlock()

	var values []string
	for _, source := range sources {
		values = append(values, source()...)
	}
	return values
}

// SensitiveDataReplacer returns the replacer FilterSensitiveData applies. It
// replaces the config's own secrets and the registered sources' current
// values; the replacer is rebuilt only when those values change.
func (sec *Config) SensitiveDataReplacer() *strings.Replacer {
	cache := sec.sensitiveDataCache()
	cache.once.Do(func() {
		cache.values = sec.collectSensitiveValues()
	})
	values := filterableSensitiveValues(cache.values, registeredSensitiveValues())

	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.replacer == nil || !slices.Equal(values, cache.built) {
		pairs := make([]string, 0, 2*len(values))
		for _, value := range values {
			pairs = append(pairs, value, "[FILTERED]")
		}
		cache.replacer = strings.NewReplacer(pairs...)
		cache.built = values
	}
	return cache.replacer
}

// sensitiveDataCache returns the config's cache, creating it on first use.
func (sec *Config) sensitiveDataCache() *SensitiveDataCache {
	sensitiveCacheInitMu.Lock()
	defer sensitiveCacheInitMu.Unlock()
	if sec.sensitiveCache == nil {
		sec.sensitiveCache = &SensitiveDataCache{}
	}
	return sec.sensitiveCache
}

// filterableSensitiveValues returns the distinct values longer than three
// bytes, longest first, so a secret that contains another is replaced whole.
func filterableSensitiveValues(groups ...[]string) []string {
	seen := make(map[string]struct{})
	var values []string
	for _, group := range groups {
		for _, value := range group {
			if len(value) <= 3 {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			values = append(values, value)
		}
	}
	slices.SortFunc(values, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return values
}

// collectSensitiveValues collects all sensitive strings from SecurityConfig using reflection.
func (sec *Config) collectSensitiveValues() []string {
	var values []string
	collectSensitive(reflect.ValueOf(sec), &values)
	return append(values, sec.secretMapValues()...)
}

// collectSensitive recursively traverses the value and collects SecureString/SecureStrings values.
func collectSensitive(v reflect.Value, values *[]string) {
	for v.Kind() == reflect.Ptr || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}

	t := v.Type()

	// Channel: use CollectSensitiveValues() method
	if t == reflect.TypeOf(Channel{}) {
		if method := v.MethodByName("CollectSensitiveValues"); method.IsValid() {
			results := method.Call(nil)
			if len(results) > 0 {
				if vals, ok := results[0].Interface().([]string); ok {
					*values = append(*values, vals...)
				}
			}
		}
		return
	}

	// SecureString: collect via String() method (defined on *SecureString)
	if t == reflect.TypeOf(SecureString{}) {
		// Create a new pointer to make it addressable for method calls
		ptr := reflect.New(t)
		ptr.Elem().Set(v)
		result := ptr.MethodByName("String").Call(nil)
		if len(result) > 0 {
			if s := result[0].String(); s != "" {
				*values = append(*values, s)
			}
		}
		return
	}

	// SecureStrings ([]*SecureString): iterate and collect each element
	if t == reflect.TypeOf(SecureStrings{}) {
		for i := 0; i < v.Len(); i++ {
			elem := v.Index(i)
			for elem.Kind() == reflect.Ptr || elem.Kind() == reflect.Interface {
				if elem.IsNil() {
					elem = reflect.Value{}
					break
				}
				elem = elem.Elem()
			}
			if elem.IsValid() && elem.Type() == reflect.TypeOf(SecureString{}) {
				result := elem.Addr().MethodByName("String").Call(nil)
				if len(result) > 0 {
					if s := result[0].String(); s != "" {
						*values = append(*values, s)
					}
				}
			}
		}
		return
	}

	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if !t.Field(i).IsExported() {
				continue
			}
			collectSensitive(v.Field(i), values)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			collectSensitive(v.Index(i), values)
		}
	case reflect.Map:
		for _, key := range v.MapKeys() {
			collectSensitive(v.MapIndex(key), values)
		}
	}
}
