package config

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// SecretPlaceholder stands, in config.json and any other JSON of the config,
// for a secret whose value is kept in .security.yml. Sent back in an edit,
// it keeps that value.
const SecretPlaceholder = "[NOT_HERE]"

// maskSecretValues returns m with every non-empty value replaced by the
// placeholder; the names stay visible.
func maskSecretValues(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v != "" {
			v = SecretPlaceholder
		}
		out[k] = v
	}
	return out
}

// MarshalJSON writes the server with its env and header values masked: they
// often hold tokens, which are kept in .security.yml.
func (c MCPServerConfig) MarshalJSON() ([]byte, error) {
	type plain MCPServerConfig
	out := plain(c)
	out.Env = maskSecretValues(c.Env)
	out.Headers = maskSecretValues(c.Headers)
	return json.Marshal(out)
}

// MarshalJSON writes the hook with its env values masked; they are kept in
// .security.yml.
func (c ProcessHookConfig) MarshalJSON() ([]byte, error) {
	type plain ProcessHookConfig
	out := plain(c)
	out.Env = maskSecretValues(c.Env)
	return json.Marshal(out)
}

// MarshalJSON writes the instance with its header values masked; they often
// hold credentials, which are kept in .security.yml.
func (c ProviderInstanceConfig) MarshalJSON() ([]byte, error) {
	type plain ProviderInstanceConfig
	out := plain(c)
	out.Headers = maskSecretValues(c.Headers)
	return json.Marshal(out)
}

// secretMaps holds the values of one entry's secret-bearing maps.
type secretMaps struct {
	Env     map[string]SecureString `yaml:"env,omitempty"`
	Headers map[string]SecureString `yaml:"headers,omitempty"`
}

// secretMapsFile is the part of .security.yml that holds the values of the
// maps config.json shows masked, keyed by MCP server, process hook and
// provider instance id.
type secretMapsFile struct {
	MCP struct {
		Servers map[string]secretMaps `yaml:"servers,omitempty"`
	} `yaml:"mcp,omitempty"`
	Hooks struct {
		Processes map[string]secretMaps `yaml:"processes,omitempty"`
	} `yaml:"hooks,omitempty"`
	ProviderInstances map[string]secretMaps `yaml:"provider_instances,omitempty"`
}

func (f *secretMapsFile) empty() bool {
	return len(f.MCP.Servers) == 0 && len(f.Hooks.Processes) == 0 && len(f.ProviderInstances) == 0
}

// storedSecrets returns the values of m that belong in .security.yml, given
// the values the file holds now (stored). A placeholder takes the stored
// value as written, as does a value that is still what the stored one
// resolves to, so that a file:// reference stays one; any other value is
// written as given. Empty values and placeholders nothing fills are left
// out.
func storedSecrets(m map[string]string, stored map[string]SecureString) map[string]SecureString {
	var out map[string]SecureString
	for k, v := range m {
		s, ok := stored[k]
		switch {
		case ok && s.raw != "" && (v == SecretPlaceholder || v == s.String()):
			// Unchanged: keep the stored value as written.
		case v == "" || v == SecretPlaceholder:
			continue
		default:
			s = SecureString{resolved: v}
		}
		if out == nil {
			out = make(map[string]SecureString, len(m))
		}
		out[k] = s
	}
	return out
}

func addSecretMaps(dst *map[string]secretMaps, key string, m secretMaps) {
	if key == "" || (len(m.Env) == 0 && len(m.Headers) == 0) {
		return
	}
	if *dst == nil {
		*dst = make(map[string]secretMaps)
	}
	(*dst)[key] = m
}

// secretMapsForSave collects the secret map values c holds, keeping the
// written form of those stored holds unchanged (see storedSecrets).
func (c *Config) secretMapsForSave(stored secretMapsFile) secretMapsFile {
	var f secretMapsFile
	for name, server := range c.Tools.MCP.Servers {
		was := stored.MCP.Servers[name]
		addSecretMaps(&f.MCP.Servers, name, secretMaps{
			Env:     storedSecrets(server.Env, was.Env),
			Headers: storedSecrets(server.Headers, was.Headers),
		})
	}
	for name, hook := range c.Hooks.Processes {
		addSecretMaps(&f.Hooks.Processes, name, secretMaps{Env: storedSecrets(hook.Env, stored.Hooks.Processes[name].Env)})
	}
	for _, instance := range c.ProviderInstances {
		if instance != nil {
			was := stored.ProviderInstances[instance.ID]
			addSecretMaps(&f.ProviderInstances, instance.ID, secretMaps{Headers: storedSecrets(instance.Headers, was.Headers)})
		}
	}
	return f
}

// fillSecretPlaceholders returns m with each placeholder replaced by the
// value stored for its name, and whether anything changed. With drop, a
// placeholder nothing fills is removed. m itself is not modified.
func fillSecretPlaceholders(m map[string]string, stored map[string]SecureString, drop bool) (map[string]string, bool) {
	changed := false
	for k, v := range m {
		if v != SecretPlaceholder {
			continue
		}
		if _, ok := stored[k]; ok || drop {
			changed = true
			break
		}
	}
	if !changed {
		return m, false
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if v == SecretPlaceholder {
			if s, ok := stored[k]; ok {
				v = s.String()
			} else if drop {
				continue
			}
		}
		out[k] = v
	}
	return out, true
}

// applySecretMaps fills the placeholders of c's secret maps from f. With
// drop, the placeholders f cannot fill are removed, so that no consumer ever
// sees one as a value.
func (c *Config) applySecretMaps(f secretMapsFile, drop bool) {
	for name, server := range c.Tools.MCP.Servers {
		stored := f.MCP.Servers[name]
		env, envChanged := fillSecretPlaceholders(server.Env, stored.Env, drop)
		headers, headersChanged := fillSecretPlaceholders(server.Headers, stored.Headers, drop)
		if envChanged || headersChanged {
			server.Env, server.Headers = env, headers
			c.Tools.MCP.Servers[name] = server
		}
	}
	for name, hook := range c.Hooks.Processes {
		if env, changed := fillSecretPlaceholders(hook.Env, f.Hooks.Processes[name].Env, drop); changed {
			hook.Env = env
			c.Hooks.Processes[name] = hook
		}
	}
	for _, instance := range c.ProviderInstances {
		if instance == nil {
			continue
		}
		if headers, changed := fillSecretPlaceholders(instance.Headers, f.ProviderInstances[instance.ID].Headers, drop); changed {
			instance.Headers = headers
		}
	}
}

// hasSecretPlaceholders reports whether a secret map of c still holds a
// placeholder, as a config decoded from masked JSON does.
func (c *Config) hasSecretPlaceholders() bool {
	has := func(m map[string]string) bool {
		for _, v := range m {
			if v == SecretPlaceholder {
				return true
			}
		}
		return false
	}
	for _, server := range c.Tools.MCP.Servers {
		if has(server.Env) || has(server.Headers) {
			return true
		}
	}
	for _, hook := range c.Hooks.Processes {
		if has(hook.Env) {
			return true
		}
	}
	for _, instance := range c.ProviderInstances {
		if instance != nil && has(instance.Headers) {
			return true
		}
	}
	return false
}

// readSecretMaps reads the secret map values the .security.yml beside
// configPath holds; there are none when the file does not exist.
func readSecretMaps(configPath string) (secretMapsFile, error) {
	var stored secretMapsFile
	data, err := os.ReadFile(securityPath(configPath))
	if errors.Is(err, os.ErrNotExist) {
		return stored, nil
	}
	if err != nil {
		return stored, err
	}
	if err := yaml.Unmarshal(data, &stored); err != nil {
		return secretMapsFile{}, err
	}
	return stored, nil
}

// appendYAMLMapping appends the entries of the mapping src to the mapping
// dst.
func appendYAMLMapping(dst *yaml.Node, src any) error {
	var node yaml.Node
	if err := node.Encode(src); err != nil {
		return err
	}
	if node.Kind == yaml.MappingNode && dst.Kind == yaml.MappingNode {
		dst.Content = append(dst.Content, node.Content...)
	}
	return nil
}

// secretMapValues returns the values of c's secret maps, for filtering.
// Values that can only be a flag or a number, such as "true" or "8080", are
// left out: masking them everywhere would hide much and protect nothing.
func (c *Config) secretMapValues() []string {
	var values []string
	add := func(m map[string]string) {
		for _, v := range m {
			v = strings.TrimSpace(v)
			if v == "" || v == SecretPlaceholder {
				continue
			}
			if _, err := strconv.ParseBool(v); err == nil {
				continue
			}
			if _, err := strconv.ParseFloat(v, 64); err == nil {
				continue
			}
			values = append(values, v)
		}
	}
	for _, server := range c.Tools.MCP.Servers {
		add(server.Env)
		add(server.Headers)
	}
	for _, hook := range c.Hooks.Processes {
		add(hook.Env)
	}
	for _, instance := range c.ProviderInstances {
		if instance != nil {
			add(instance.Headers)
		}
	}
	return values
}
