package config

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/xibodev/compa/pkg/credential"
	"github.com/xibodev/compa/pkg/logger"
)

// FlexibleStringSlice is a []string that also accepts JSON numbers,
// so allow_from can contain both "123" and 123.
// It also supports parsing comma-separated strings from environment variables,
// including both English (,) and Chinese (，) commas.
type FlexibleStringSlice []string

func (f *FlexibleStringSlice) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*f = nil
		return nil
	}

	// Accept a single JSON string for convenience, e.g.:
	// "text": "Thinking..."
	var singleString string
	if err := json.Unmarshal(data, &singleString); err == nil {
		*f = FlexibleStringSlice{singleString}
		return nil
	}

	// Accept a single JSON number too, to keep symmetry with mixed allow_from
	// payloads that may contain numeric identifiers.
	var singleNumber float64
	if err := json.Unmarshal(data, &singleNumber); err == nil {
		*f = FlexibleStringSlice{fmt.Sprintf("%.0f", singleNumber)}
		return nil
	}

	// Try []string first
	var ss []string
	if err := json.Unmarshal(data, &ss); err == nil {
		*f = ss
		return nil
	}

	// Try []interface{} to handle mixed types
	var raw []any
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	result := make([]string, 0, len(raw))
	for _, v := range raw {
		switch val := v.(type) {
		case string:
			result = append(result, val)
		case float64:
			result = append(result, fmt.Sprintf("%.0f", val))
		default:
			result = append(result, fmt.Sprintf("%v", val))
		}
	}
	*f = result
	return nil
}

// UnmarshalText implements encoding.TextUnmarshaler to support env variable parsing.
// It handles comma-separated values with both English (,) and Chinese (，) commas.
func (f *FlexibleStringSlice) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*f = nil
		return nil
	}

	s := string(text)
	// Replace Chinese comma with English comma, then split
	s = strings.ReplaceAll(s, "，", ",")
	parts := strings.Split(s, ",")

	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	*f = result
	return nil
}

// notHere is SecretPlaceholder as a JSON string.
const notHere = `"` + SecretPlaceholder + `"`

// SecureStrings is a slice of SecureString
//
//nolint:recvcheck
type SecureStrings []*SecureString

// IsZero reports whether the list is empty.
func (s SecureStrings) IsZero() bool {
	return len(s) == 0
}

// Values returns the decrypted/resolved values
func (s *SecureStrings) Values() []string {
	if s == nil {
		return nil
	}
	keys := make([]string, len(*s))
	for i, k := range *s {
		keys[i] = k.String()
	}
	return unique(keys)
}

func SimpleSecureStrings(val ...string) SecureStrings {
	val = unique(val)
	vv := make(SecureStrings, len(val))
	for i, s := range val {
		vv[i] = NewSecureString(s)
	}
	return vv
}

// unique returns a new slice with duplicate elements removed.
func unique[T comparable](input []T) []T {
	m := make(map[T]struct{})
	var result []T
	for _, v := range input {
		if _, ok := m[v]; !ok {
			m[v] = struct{}{}
			result = append(result, v)
		}
	}
	return result
}

func (s SecureStrings) MarshalJSON() ([]byte, error) {
	return []byte(notHere), nil
}

// UnmarshalJSON reads a list of values. The placeholder, for the whole list
// or an entry, reads as unset: the values are in .security.yml.
func (s *SecureStrings) UnmarshalJSON(value []byte) error {
	if string(value) == notHere {
		*s = nil
		return nil
	}
	var v []*SecureString
	err := json.Unmarshal(value, &v)
	if err != nil {
		return err
	}
	// Leave out the entries that read as unset, such as placeholders.
	filtered := make(SecureStrings, 0, len(v))
	for _, ss := range v {
		if ss == nil {
			continue
		}
		if ss.resolved != "" || ss.raw != "" {
			filtered = append(filtered, ss)
		}
	}
	if len(filtered) == 0 {
		*s = nil
	} else {
		*s = filtered
	}
	return nil
}

// SecureString is a secret setting. JSON shows a placeholder instead of a
// value that is set; the value is kept in .security.yml, as written or as a
// file:// reference to a file beside it.
//
//nolint:recvcheck
type SecureString struct {
	resolved string // The value String returns; a file:// reference is read
	raw      string // The value as written: plain or a file:// reference
}

// IsZero reports whether the value is empty.
func (s SecureString) IsZero() bool {
	return s.resolved == ""
}

func NewSecureString(value string) *SecureString {
	s := &SecureString{}
	if err := s.fromRaw(value); err != nil {
		logger.Warn(fmt.Sprintf("NewSecureString.fromRaw error: %s", err))
	}
	return s
}

func (s *SecureString) String() string {
	if s == nil {
		return ""
	}
	return s.resolved
}

func (s *SecureString) Set(value string) *SecureString {
	s.resolved = value
	s.raw = ""
	return s
}

func (s SecureString) MarshalJSON() ([]byte, error) {
	return []byte(notHere), nil
}

// UnmarshalJSON reads a plain value or a file:// reference. The placeholder
// reads as unset: the value is in .security.yml.
func (s *SecureString) UnmarshalJSON(value []byte) error {
	var v string
	if err := json.Unmarshal(value, &v); err != nil {
		return err
	}
	return s.fromRaw(v)
}

// MarshalYAML writes the value for .security.yml: a file:// reference as
// the reference, any other value as it is.
func (s SecureString) MarshalYAML() (any, error) {
	if strings.HasPrefix(s.raw, credential.FileScheme) {
		return s.raw, nil
	}
	return s.resolved, nil
}

func (s *SecureString) UnmarshalYAML(value *yaml.Node) error {
	return s.fromRaw(value.Value)
}

// fromRaw sets the value from its written form; the placeholder, which only
// stands for a value kept elsewhere, reads as unset.
func (s *SecureString) fromRaw(v string) error {
	if v == SecretPlaceholder {
		*s = SecureString{}
		return nil
	}
	s.raw = v
	vv, err := resolveKey(v)
	if err != nil {
		return err
	}
	s.resolved = vv
	return nil
}

var (
	secResolverMu sync.RWMutex
	secResolver   *credential.Resolver
)

func updateResolver(path string) {
	secResolverMu.Lock()
	defer secResolverMu.Unlock()
	secResolver = credential.NewResolver(path)
}

// resolveKey returns the value v stands for: the content of the file a
// file:// reference names, or v itself.
func resolveKey(v string) (string, error) {
	if !strings.HasPrefix(v, credential.FileScheme) {
		return v, nil
	}
	secResolverMu.RLock()
	resolver := secResolver
	secResolverMu.RUnlock()
	if resolver == nil {
		resolver = credential.NewResolver("")
	}
	resolved, err := resolver.Resolve(v)
	if err != nil {
		logger.Errorf("Resolve error: %v", err)
		return "", err
	}
	return resolved, nil
}

func (s *SecureString) UnmarshalText(text []byte) error {
	v := string(text)
	return s.fromRaw(v)
}

func (v *SkillsRegistriesConfig) UnmarshalJSON(data []byte) error {
	var list []json.RawMessage
	if err := json.Unmarshal(data, &list); err == nil {
		decodedList := make([]*SkillRegistryConfig, 0, len(list))
		for _, item := range list {
			var nameOnly struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal(item, &nameOnly); err != nil {
				return err
			}
			registry := cloneRegistryConfig(findRegistryConfigByName(*v, nameOnly.Name))
			if registry == nil {
				registry = &SkillRegistryConfig{Name: nameOnly.Name}
			}
			if err := json.Unmarshal(item, registry); err != nil {
				return err
			}
			decodedList = append(decodedList, registry)
		}
		if len(*v) > 0 {
			for _, registry := range decodedList {
				if registry == nil {
					continue
				}
				v.Set(registry.Name, *registry)
			}
			return nil
		}
		*v = decodedList
		return nil
	}

	byName := map[string]json.RawMessage{}
	if err := json.Unmarshal(data, &byName); err != nil {
		return err
	}

	if len(*v) == 0 {
		keys := make([]string, 0, len(byName))
		for name := range byName {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		decodedList := make([]*SkillRegistryConfig, 0, len(keys))
		for _, name := range keys {
			var registry SkillRegistryConfig
			if err := json.Unmarshal(byName[name], &registry); err != nil {
				return err
			}
			registry.Name = name
			decodedList = append(decodedList, &registry)
		}
		*v = decodedList
		return nil
	}

	for _, name := range sortedRegistryNamesFromJSON(byName) {
		registry := cloneRegistryConfig(findRegistryConfigByName(*v, name))
		if registry == nil {
			registry = &SkillRegistryConfig{Name: name}
		}
		if err := json.Unmarshal(byName[name], registry); err != nil {
			return err
		}
		registry.Name = name
		v.Set(name, *registry)
	}
	return nil
}

func (v SkillsRegistriesConfig) MarshalJSON() ([]byte, error) {
	if v == nil {
		return []byte("null"), nil
	}
	mm := make(map[string]SkillRegistryConfig, len(v))
	for _, registry := range v {
		if registry == nil || registry.Name == "" {
			continue
		}
		mm[registry.Name] = *registry
	}
	return json.Marshal(mm)
}

func (c *SkillRegistryConfig) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	params := cloneRegistryParams(c.Param)
	if params == nil {
		params = map[string]any{}
	}
	if value, ok := raw["name"]; ok {
		if err := json.Unmarshal(value, &c.Name); err != nil {
			return err
		}
	}
	if value, ok := raw["enabled"]; ok {
		if err := json.Unmarshal(value, &c.Enabled); err != nil {
			return err
		}
	}
	if value, ok := raw["base_url"]; ok {
		if err := json.Unmarshal(value, &c.BaseURL); err != nil {
			return err
		}
	}
	if value, ok := raw["auth_token"]; ok {
		if err := json.Unmarshal(value, &c.AuthToken); err != nil {
			return err
		}
	}
	if value, ok := raw["param"]; ok {
		var nested map[string]any
		if err := json.Unmarshal(value, &nested); err != nil {
			return err
		}
		for key, nestedValue := range nested {
			params[key] = nestedValue
		}
	}
	for key, value := range raw {
		switch key {
		case "name", "enabled", "base_url", "auth_token", "param":
			continue
		case "_auth_token":
			// UI/API shadow secret fields should hydrate SecureString only and must
			// never be persisted as arbitrary registry params.
			continue
		default:
			var decoded any
			if err := json.Unmarshal(value, &decoded); err != nil {
				return err
			}
			params[key] = decoded
		}
	}
	c.Param = params
	return nil
}

func (c SkillRegistryConfig) MarshalJSON() ([]byte, error) {
	m := map[string]any{
		"enabled":  c.Enabled,
		"base_url": c.BaseURL,
	}
	if c.AuthToken.String() != "" {
		m["auth_token"] = c.AuthToken
	}
	for key, value := range c.Param {
		if key == "" || key == "param" || strings.HasPrefix(key, "_") {
			continue
		}
		if _, exists := m[key]; exists {
			continue
		}
		m[key] = value
	}
	return json.Marshal(m)
}

func (c *SkillRegistryConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw map[string]any
	if err := value.Decode(&raw); err != nil {
		return err
	}
	params := cloneRegistryParams(c.Param)
	if params == nil {
		params = map[string]any{}
	}
	if nested, ok := raw["param"].(map[string]any); ok {
		for k, v := range nested {
			params[k] = v
		}
	}
	for key, v := range raw {
		switch key {
		case "name":
			if s, ok := v.(string); ok {
				c.Name = s
			}
		case "enabled":
			if b, ok := v.(bool); ok {
				c.Enabled = b
			}
		case "base_url":
			if s, ok := v.(string); ok {
				c.BaseURL = s
			}
		case "auth_token":
			data, err := yaml.Marshal(v)
			if err != nil {
				return err
			}
			if err := yaml.Unmarshal(data, &c.AuthToken); err != nil {
				return err
			}
		case "_auth_token":
			// UI/API shadow secret fields should hydrate SecureString only and must
			// never be persisted as arbitrary registry params.
			continue
		case "param":
			continue
		default:
			params[key] = v
		}
	}
	c.Param = params
	return nil
}

func (c SkillRegistryConfig) MarshalYAML() (any, error) {
	m := map[string]any{
		"enabled":  c.Enabled,
		"base_url": c.BaseURL,
	}
	if c.AuthToken.String() != "" {
		m["auth_token"] = c.AuthToken
	}
	keys := make([]string, 0, len(c.Param))
	for key := range c.Param {
		if key == "" || key == "param" || strings.HasPrefix(key, "_") {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := m[key]; exists {
			continue
		}
		m[key] = c.Param[key]
	}
	return m, nil
}

func (v *SkillsRegistriesConfig) UnmarshalYAML(value *yaml.Node) error {
	decoded, err := decodeRegistryNodesFromYAML(value, nil)
	if err != nil {
		logger.Errorf("Decode error: %v", err)
		return err
	}
	if len(*v) == 0 {
		keys := make([]string, 0, len(decoded))
		for name := range decoded {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		list := make([]*SkillRegistryConfig, 0, len(keys))
		for _, name := range keys {
			registry := decoded[name]
			if registry == nil {
				continue
			}
			list = append(list, registry)
		}
		*v = list
		return nil
	}
	decoded, err = decodeRegistryNodesFromYAML(value, *v)
	if err != nil {
		logger.Errorf("Decode error: %v", err)
		return err
	}
	for _, name := range sortedRegistryNames(decoded) {
		registry := decoded[name]
		if registry == nil {
			continue
		}
		v.Set(name, *registry)
	}
	return nil
}

func decodeRegistryNodesFromYAML(
	value *yaml.Node,
	existing SkillsRegistriesConfig,
) (map[string]*SkillRegistryConfig, error) {
	decoded := make(map[string]*SkillRegistryConfig)
	if value == nil {
		return decoded, nil
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		nameNode := value.Content[i]
		registryNode := value.Content[i+1]
		if nameNode == nil || registryNode == nil {
			continue
		}
		name := strings.TrimSpace(nameNode.Value)
		if name == "" {
			continue
		}
		registry := cloneRegistryConfig(findRegistryConfigByName(existing, name))
		if registry == nil {
			registry = &SkillRegistryConfig{Name: name}
		}
		if err := registryNode.Decode(registry); err != nil {
			return nil, err
		}
		registry.Name = name
		decoded[name] = registry
	}
	return decoded, nil
}

func cloneRegistryParams(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	cloned := make(map[string]any, len(src))
	for key, value := range src {
		cloned[key] = value
	}
	return cloned
}

func cloneRegistryConfig(src *SkillRegistryConfig) *SkillRegistryConfig {
	if src == nil {
		return nil
	}
	cloned := *src
	cloned.Param = cloneRegistryParams(src.Param)
	return &cloned
}

func findRegistryConfigByName(registries SkillsRegistriesConfig, name string) *SkillRegistryConfig {
	for _, registry := range registries {
		if registry == nil || registry.Name != name {
			continue
		}
		return registry
	}
	return nil
}

func sortedRegistryNames(mm map[string]*SkillRegistryConfig) []string {
	keys := make([]string, 0, len(mm))
	for name := range mm {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

func sortedRegistryNamesFromJSON(mm map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(mm))
	for name := range mm {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	return keys
}

func (v SkillsRegistriesConfig) MarshalYAML() (any, error) {
	type onlySecureRegistryData struct {
		AuthToken SecureString `yaml:"auth_token,omitempty"`
	}
	mm := make(map[string]onlySecureRegistryData)
	for _, registry := range v {
		if registry == nil || registry.Name == "" {
			continue
		}
		if registry.AuthToken.String() == "" {
			continue
		}
		mm[registry.Name] = onlySecureRegistryData{
			AuthToken: registry.AuthToken,
		}
	}

	return mm, nil
}
