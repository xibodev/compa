package launcherconfig

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const (
	// FileName is the launcher-specific settings file name.
	FileName = "launcher-config.json"
	// DefaultPort is the default port for the web launcher.
	DefaultPort = 18800
	// EnvLauncherHost overrides launcher listen host.
	EnvLauncherHost = "COMPA_LAUNCHER_HOST"
)

// Config stores launch parameters for the web backend service.
type Config struct {
	Port                       int             `json:"port"`
	Public                     bool            `json:"public"`
	AllowedCIDRs               []string        `json:"allowed_cidrs,omitempty"`
	AllowLocalhostBypass       bool            `json:"allow_localhost_bypass"`
	AllowLocalhostBypassSource BoolFieldSource `json:"-"`
	TrustedProxyCIDRs          []string        `json:"trusted_proxy_cidrs,omitempty"`
	DashboardPasswordHash      string          `json:"dashboard_password_hash,omitempty"`
	// AllowedHosts are extra Host header names the dashboard answers to, such
	// as a reverse proxy's name. Loopback names, this computer's addresses and
	// the listen host are always accepted.
	AllowedHosts []string `json:"allowed_hosts,omitempty"`
	// AllowLANWithoutPassword lets public (LAN) mode start before a dashboard
	// password is set. Off, the first visitor could set it.
	AllowLANWithoutPassword bool `json:"allow_lan_without_password,omitempty"`
	// RemoteImages is how chat replies show images from other sites:
	// "click" (load when clicked) or "always".
	RemoteImages string `json:"remote_images,omitempty"`
}

// Values of Config.RemoteImages.
const (
	RemoteImagesClick  = "click"
	RemoteImagesAlways = "always"
)

// EffectiveRemoteImages returns RemoteImages, or "click" when unset.
func (c Config) EffectiveRemoteImages() string {
	if strings.TrimSpace(c.RemoteImages) == RemoteImagesAlways {
		return RemoteImagesAlways
	}
	return RemoteImagesClick
}

// NormalizeHosts trims, lowercases and deduplicates host names, dropping
// empty entries and any port.
func NormalizeHosts(hosts []string) []string {
	if len(hosts) == 0 {
		return nil
	}
	out := make([]string, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for _, raw := range hosts {
		host := strings.ToLower(strings.TrimSpace(raw))
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		if host == "" {
			continue
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		out = append(out, host)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// BoolFieldSource tracks whether a JSON boolean field was omitted, explicitly
// provided, or explicitly set to null. This is only used for diagnostics.
type BoolFieldSource uint8

const (
	BoolFieldAbsent BoolFieldSource = iota
	BoolFieldPresent
	BoolFieldNull
)

// Default returns default launcher settings.
func Default() Config {
	return Config{Port: DefaultPort, Public: false, AllowLocalhostBypass: true}
}

// Validate checks if launcher settings are valid.
func Validate(cfg Config) error {
	if cfg.Port < 1 || cfg.Port > 65535 {
		return fmt.Errorf("port %d is out of range (1-65535)", cfg.Port)
	}
	for _, cidr := range cfg.AllowedCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q", cidr)
		}
	}
	for _, cidr := range cfg.TrustedProxyCIDRs {
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid trusted proxy CIDR %q", cidr)
		}
	}
	switch strings.TrimSpace(cfg.RemoteImages) {
	case "", RemoteImagesClick, RemoteImagesAlways:
	default:
		return fmt.Errorf("remote_images %q must be click or always", cfg.RemoteImages)
	}
	return nil
}

// NormalizeCIDRs trims entries, removes empty values, and deduplicates CIDRs.
func NormalizeCIDRs(cidrs []string) []string {
	if len(cidrs) == 0 {
		return nil
	}
	out := make([]string, 0, len(cidrs))
	seen := make(map[string]struct{}, len(cidrs))
	for _, raw := range cidrs {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// PathForAppConfig returns launcher-config path near the app config file.
func PathForAppConfig(appConfigPath string) string {
	dir := filepath.Dir(appConfigPath)
	if dir == "" || dir == "." {
		dir = "."
	}
	return filepath.Join(dir, FileName)
}

// Load reads launcher settings; fallback is returned when file does not exist.
func Load(path string, fallback Config) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fallback, nil
		}
		return Config{}, err
	}

	cfg := fallback
	cfg.AllowLocalhostBypassSource = detectBoolFieldSource(data, "allow_localhost_bypass")
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	cfg.AllowedCIDRs = NormalizeCIDRs(cfg.AllowedCIDRs)
	cfg.TrustedProxyCIDRs = NormalizeCIDRs(cfg.TrustedProxyCIDRs)
	cfg.AllowedHosts = NormalizeHosts(cfg.AllowedHosts)
	cfg.DashboardPasswordHash = strings.TrimSpace(cfg.DashboardPasswordHash)
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func detectBoolFieldSource(data []byte, field string) BoolFieldSource {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return BoolFieldAbsent
	}

	value, ok := raw[field]
	if !ok {
		return BoolFieldAbsent
	}

	if string(value) == "null" {
		return BoolFieldNull
	}

	return BoolFieldPresent
}

// Save writes launcher settings to disk.
func Save(path string, cfg Config) error {
	cfg.AllowedCIDRs = NormalizeCIDRs(cfg.AllowedCIDRs)
	cfg.TrustedProxyCIDRs = NormalizeCIDRs(cfg.TrustedProxyCIDRs)
	cfg.AllowedHosts = NormalizeHosts(cfg.AllowedHosts)
	cfg.DashboardPasswordHash = strings.TrimSpace(cfg.DashboardPasswordHash)
	if err := Validate(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o600)
}
