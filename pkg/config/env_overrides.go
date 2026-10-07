package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
)

// envOverride is a setting the environment changed while the config was
// loaded. SaveConfig writes before, the value config.json or .security.yml
// held, for as long as the setting still has the value the environment set,
// so an override that exists only in the environment never reaches a file.
type envOverride struct {
	// locate finds the setting in a config; it reports false when the
	// config no longer has it.
	locate func(c *Config) (reflect.Value, bool)
	before reflect.Value
	after  reflect.Value
}

// InheritEnvOverrides makes SaveConfig treat c's settings as overridden by
// the environment wherever loaded's were. A config rebuilt from a loaded one,
// for example decoded from its JSON, carries the environment's values but
// not the record of them; without this, saving it would write those values
// to the files.
func (c *Config) InheritEnvOverrides(loaded *Config) {
	if c == nil || loaded == nil || c == loaded {
		return
	}
	c.envOverrides = append([]envOverride(nil), loaded.envOverrides...)
}

// copyForSave returns a deep copy of c as the files should hold it: with the
// file's own value wherever the environment overrode a setting that has not
// changed since.
func (c *Config) copyForSave() *Config {
	out := deepCopy(reflect.ValueOf(c).Elem()).Addr().Interface().(*Config)
	for _, o := range c.envOverrides {
		field, ok := o.locate(out)
		if !ok || !field.CanSet() || field.Type() != o.after.Type() {
			continue
		}
		if equalSettingValues(field, o.after) {
			field.Set(deepCopy(o.before))
		}
	}
	return out
}

// envSnapshot holds the values of the settings the environment may change,
// taken before the overrides are applied.
type envSnapshot struct {
	fields     map[string]reflect.Value
	registries SkillsRegistriesConfig
	host       string
}

func (c *Config) snapshotEnvSettings() envSnapshot {
	root := reflect.ValueOf(c).Elem()
	snap := envSnapshot{fields: make(map[string]reflect.Value), host: c.Gateway.Host}
	for _, path := range envFieldPaths(root.Type()) {
		if field, err := root.FieldByIndexErr(path); err == nil {
			snap.fields[pathKey(path)] = deepCopy(field)
		}
	}
	snap.registries = deepCopy(reflect.ValueOf(c.Tools.Skills.Registries)).Interface().(SkillsRegistriesConfig)
	return snap
}

// recordEnvOverrides compares c with the snapshot taken before the
// environment was applied and records every setting it changed.
func (c *Config) recordEnvOverrides(snap envSnapshot) {
	root := reflect.ValueOf(c).Elem()
	hostPath := gatewayHostPath()
	for _, path := range envFieldPaths(root.Type()) {
		if pathKey(path) == pathKey(hostPath) {
			continue
		}
		before, ok := snap.fields[pathKey(path)]
		if !ok {
			continue
		}
		field, err := root.FieldByIndexErr(path)
		if err != nil || equalSettingValues(before, field) {
			continue
		}
		c.envOverrides = append(c.envOverrides, envOverride{
			locate: locateConfigField(path),
			before: before,
			after:  deepCopy(field),
		})
	}

	// The gateway host is normalized whether or not the environment sets it;
	// only an environment value is an override.
	if host, ok := os.LookupEnv(EnvGatewayHost); ok && strings.TrimSpace(host) != "" {
		fileHost, err := normalizeGatewayHostInput(snap.host)
		if err != nil {
			fileHost = snap.host
		}
		if fileHost != c.Gateway.Host {
			c.envOverrides = append(c.envOverrides, envOverride{
				locate: locateConfigField(hostPath),
				before: reflect.ValueOf(fileHost),
				after:  reflect.ValueOf(c.Gateway.Host),
			})
		}
	}

	// The skills registries are a list, which env tags cannot address; the
	// COMPA_SKILLS_REGISTRIES_* variables set their fields directly.
	for _, registry := range c.Tools.Skills.Registries {
		if registry == nil {
			continue
		}
		previous := findRegistryConfigByName(snap.registries, registry.Name)
		if previous == nil {
			continue
		}
		now := reflect.ValueOf(registry).Elem()
		then := reflect.ValueOf(previous).Elem()
		for _, name := range []string{"Enabled", "BaseURL", "AuthToken", "Param"} {
			if equalSettingValues(then.FieldByName(name), now.FieldByName(name)) {
				continue
			}
			c.envOverrides = append(c.envOverrides, envOverride{
				locate: locateRegistryField(registry.Name, name),
				before: deepCopy(then.FieldByName(name)),
				after:  deepCopy(now.FieldByName(name)),
			})
		}
	}
}

// channelEnvRecorder records the environment's changes to one channel's
// decoded settings.
type channelEnvRecorder struct {
	overrides *[]envOverride
}

// record compares a channel's decoded settings with their copy taken before
// the environment was applied.
func (r channelEnvRecorder) record(channel string, before, target reflect.Value) {
	if r.overrides == nil || target.Kind() != reflect.Struct {
		return
	}
	for i := range target.NumField() {
		if !target.Type().Field(i).IsExported() || equalSettingValues(before.Field(i), target.Field(i)) {
			continue
		}
		*r.overrides = append(*r.overrides, envOverride{
			locate: locateChannelField(channel, target.Type(), i),
			before: deepCopy(before.Field(i)),
			after:  deepCopy(target.Field(i)),
		})
	}
}

func locateConfigField(path []int) func(*Config) (reflect.Value, bool) {
	return func(c *Config) (reflect.Value, bool) {
		field, err := reflect.ValueOf(c).Elem().FieldByIndexErr(path)
		return field, err == nil
	}
}

func locateChannelField(channel string, settingsType reflect.Type, index int) func(*Config) (reflect.Value, bool) {
	return func(c *Config) (reflect.Value, bool) {
		ch := c.Channels[channel]
		if ch == nil {
			return reflect.Value{}, false
		}
		decoded, err := ch.GetDecoded()
		if err != nil || decoded == nil {
			return reflect.Value{}, false
		}
		settings := reflect.ValueOf(decoded)
		if settings.Kind() != reflect.Pointer || settings.Elem().Type() != settingsType {
			return reflect.Value{}, false
		}
		return settings.Elem().Field(index), true
	}
}

func locateRegistryField(registry, field string) func(*Config) (reflect.Value, bool) {
	return func(c *Config) (reflect.Value, bool) {
		found := findRegistryConfigByName(c.Tools.Skills.Registries, registry)
		if found == nil {
			return reflect.Value{}, false
		}
		return reflect.ValueOf(found).Elem().FieldByName(field), true
	}
}

func gatewayHostPath() []int {
	gateway, _ := reflect.TypeOf(Config{}).FieldByName("Gateway")
	host, _ := gateway.Type.FieldByName("Host")
	return append(append([]int(nil), gateway.Index...), host.Index...)
}

var envFieldPathCache sync.Map // reflect.Type -> [][]int

// envFieldPaths lists the index paths of the fields of t that env.Parse may
// set: those with an env tag, in t and in the structs it holds.
func envFieldPaths(t reflect.Type) [][]int {
	if cached, ok := envFieldPathCache.Load(t); ok {
		return cached.([][]int)
	}
	var paths [][]int
	var walk func(t reflect.Type, prefix []int, depth int)
	walk = func(t reflect.Type, prefix []int, depth int) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || depth > 16 {
			return
		}
		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("env"), ",")
			if name == "-" {
				continue
			}
			path := append(append([]int(nil), prefix...), i)
			if name != "" {
				paths = append(paths, path)
				continue
			}
			walk(field.Type, path, depth+1)
		}
	}
	walk(t, nil, 0)
	envFieldPathCache.Store(t, paths)
	return paths
}

func pathKey(path []int) string {
	return fmt.Sprint(path)
}
