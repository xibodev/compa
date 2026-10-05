package api

import (
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/netbind"
	"github.com/xibodev/compa/web/backend/launcherconfig"
)

// LAN mode no longer widens the kernel's bind host: the dashboard proxies what
// a browser needs, so the kernel stays where its own config puts it.
func TestGatewayBindHostStaysOnConfigInPublicMode(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	launcherPath := launcherconfig.PathForAppConfig(configPath)
	if err := launcherconfig.Save(launcherPath, launcherconfig.Config{
		Port:   18800,
		Public: true,
	}); err != nil {
		t.Fatalf("launcherconfig.Save() error = %v", err)
	}

	h := NewHandler(configPath)
	h.SetServerOptions(18800, true, true, nil)

	cfg := config.DefaultConfig()
	if got := h.effectiveGatewayBindHost(cfg); got != cfg.Gateway.Host {
		t.Fatalf("effectiveGatewayBindHost() = %q, want the config's %q", got, cfg.Gateway.Host)
	}
}

func TestGatewayProbeHostUsesLoopbackForWildcardBind(t *testing.T) {
	want := "127.0.0.1"
	if got := gatewayProbeHost("0.0.0.0"); got != want {
		t.Fatalf("gatewayProbeHost() = %q, want %q", got, want)
	}
}

func TestGatewayProbeHostUsesPreferredLoopbackForEmptyBind(t *testing.T) {
	want := netbind.ResolveAdaptiveLoopbackHost()
	if got := gatewayProbeHost(""); got != want {
		t.Fatalf("gatewayProbeHost(empty) = %q, want %q", got, want)
	}
}

func TestGatewayProbeHostUsesPreferredLoopbackForLocalhostBind(t *testing.T) {
	want := netbind.ResolveAdaptiveLoopbackHost()
	if got := gatewayProbeHost("localhost"); got != want {
		t.Fatalf("gatewayProbeHost(localhost) = %q, want %q", got, want)
	}
}

func TestGatewayProbeHostUsesLoopbackForIPv6WildcardBind(t *testing.T) {
	want := "::1"
	if got := gatewayProbeHost("::"); got != want {
		t.Fatalf("gatewayProbeHost(::) = %q, want %q", got, want)
	}
}

func TestGatewayProbeHostUsesFirstConcreteHostForMultiHostBind(t *testing.T) {
	if got := gatewayProbeHost("127.0.0.1,::1"); got != "127.0.0.1" {
		t.Fatalf("gatewayProbeHost(multi) = %q, want %q", got, "127.0.0.1")
	}
}

func TestGatewayProxyURLUsesConfiguredHost(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "192.168.1.10"
	cfg.Gateway.Port = 18791
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatalf("SaveConfig() error = %v", err)
	}

	if got := h.gatewayProxyURL().String(); got != "http://192.168.1.10:18791" {
		t.Fatalf("gatewayProxyURL() = %q, want %q", got, "http://192.168.1.10:18791")
	}
}

func TestGetGatewayHealthUsesConfiguredHost(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "192.168.1.10"
	cfg.Gateway.Port = 18791

	originalHealthGet := gatewayHealthGet
	t.Cleanup(func() {
		gatewayHealthGet = originalHealthGet
	})

	var requestedURL string
	gatewayHealthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		requestedURL = url
		return nil, errors.New("probe failed")
	}

	_, statusCode, err := h.getGatewayHealth(cfg, time.Second)
	_ = statusCode
	_ = err

	if requestedURL != "http://192.168.1.10:18791/health" {
		t.Fatalf("health url = %q, want %q", requestedURL, "http://192.168.1.10:18791/health")
	}
}

// LAN mode leaves the kernel on its configured host, so the health probe goes
// there rather than to a wildcard's loopback stand-in.
func TestGetGatewayHealthUsesTheKernelHostForPublicLauncher(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	h := NewHandler(configPath)
	h.SetServerOptions(18800, true, true, nil)

	cfg := config.DefaultConfig()
	cfg.Gateway.Host = "127.0.0.1"
	cfg.Gateway.Port = 18791

	originalHealthGet := gatewayHealthGet
	t.Cleanup(func() {
		gatewayHealthGet = originalHealthGet
	})

	var requestedURL string
	gatewayHealthGet = func(url string, timeout time.Duration) (*http.Response, error) {
		requestedURL = url
		return nil, errors.New("probe failed")
	}

	_, statusCode, err := h.getGatewayHealth(cfg, time.Second)
	_ = statusCode
	_ = err

	want := "http://" + net.JoinHostPort("127.0.0.1", "18791") + "/health"
	if requestedURL != want {
		t.Fatalf("health url = %q, want %q", requestedURL, want)
	}
}

// An explicit launcher bind host is the dashboard's; the kernel keeps binding
// where its own config says.
func TestExplicitLauncherHostDoesNotMoveGatewayBindHost(t *testing.T) {
	for _, host := range []string{"0.0.0.0", "::", "127.0.0.1,::1"} {
		h := NewHandler(filepath.Join(t.TempDir(), "config.json"))
		h.SetServerOptions(18800, false, false, nil)
		h.SetServerBindHost(host, true)

		cfg := config.DefaultConfig()
		if got := h.effectiveGatewayBindHost(cfg); got != cfg.Gateway.Host {
			t.Fatalf("launcher host %q: effectiveGatewayBindHost() = %q, want the config's %q", host, got, cfg.Gateway.Host)
		}
	}
}
