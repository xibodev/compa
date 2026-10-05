package utils

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCreateSafeHTTPClient_AllowsLoopbackProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.String() != "http://example.com/proxied" {
			t.Fatalf("proxy received URL %q, want %q", r.URL.String(), "http://example.com/proxied")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxy.Close()

	client, err := CreateSafeHTTPClient(SafeHTTPClientOptions{
		ProxyURL: proxy.URL,
		Timeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("CreateSafeHTTPClient() error: %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "http://example.com/proxied", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error: %v", err)
	}
	AllowConfiguredProxyFirstHop(req, client.Transport)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do() error: %v", err)
	}
	defer resp.Body.Close()
}

func TestCreateSafeHTTPClient_BlocksPrivateRedirect(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://10.0.0.1/secret", http.StatusFound)
	}))
	defer server.Close()

	// Only the test server is whitelisted, so the first request goes through
	// and the redirect check is what refuses the other private address.
	host, _, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort() error: %v", err)
	}
	client, err := CreateSafeHTTPClient(SafeHTTPClientOptions{
		Timeout:              5 * time.Second,
		PrivateHostWhitelist: []string{host},
		MaxRedirects:         5,
	})
	if err != nil {
		t.Fatalf("CreateSafeHTTPClient() error: %v", err)
	}

	resp, err := client.Get(server.URL)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected redirect to private host to fail")
	}
	if !strings.Contains(err.Error(), "redirect target is private or local network host") {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits.Load() != 1 {
		t.Fatalf("server hits = %d, want 1 (the first request must pass)", hits.Load())
	}
}

func TestCreateSafeHTTPClient_BlocksRedirectToPrivateName(t *testing.T) {
	stubLookup(t, map[string][]string{"intranet.test": {"10.0.0.7"}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://intranet.test/secret", http.StatusFound)
	}))
	defer server.Close()

	host, _, _ := net.SplitHostPort(server.Listener.Addr().String())
	client, err := CreateSafeHTTPClient(SafeHTTPClientOptions{
		Timeout:              5 * time.Second,
		PrivateHostWhitelist: []string{host},
	})
	if err != nil {
		t.Fatalf("CreateSafeHTTPClient() error: %v", err)
	}
	// net/http caches the environment proxy per process (another test sets
	// one), so pin this test to direct dials.
	client.Transport.(*http.Transport).Proxy = nil
	resp, err := client.Get(server.URL)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "all resolved addresses for intranet.test are private") {
		t.Fatalf("expected the dial check to refuse the redirect, got %v", err)
	}
}

// stubLookup replaces the resolver with a fixed table; unknown names fail.
func stubLookup(t *testing.T, table map[string][]string) {
	t.Helper()
	previous := lookupIPAddr
	lookupIPAddr = func(_ context.Context, host string) ([]net.IPAddr, error) {
		addrs, ok := table[host]
		if !ok {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		out := make([]net.IPAddr, 0, len(addrs))
		for _, a := range addrs {
			out = append(out, net.IPAddr{IP: net.ParseIP(a)})
		}
		return out, nil
	}
	t.Cleanup(func() { lookupIPAddr = previous })
}

func newRecordingProxy(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("proxied"))
	}))
	t.Cleanup(proxy.Close)
	return proxy, &hits
}

func TestCreateSafeHTTPClient_ProxyRefusesPrivateResolution(t *testing.T) {
	stubLookup(t, map[string][]string{
		"intranet.test": {"93.184.216.34", "10.0.0.7"},
		"public.test":   {"93.184.216.34"},
	})
	proxy, hits := newRecordingProxy(t)

	client, err := CreateSafeHTTPClient(SafeHTTPClientOptions{ProxyURL: proxy.URL, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("CreateSafeHTTPClient() error: %v", err)
	}

	get := func(rawURL string) error {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatalf("http.NewRequest() error: %v", err)
		}
		AllowConfiguredProxyFirstHop(req, client.Transport)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	// Any private answer refuses the request before the proxy sees it.
	for _, target := range []string{
		"http://intranet.test/",
		"http://10.1.2.3/",
		"http://2130706433/",
		"http://[64:ff9b::7f00:1]/",
	} {
		if err := get(target); err == nil || !strings.Contains(err.Error(), "blocked private or local target") {
			t.Fatalf("%s: expected the proxied target to be refused, got %v", target, err)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("proxy hits = %d, want 0", hits.Load())
	}

	// Public answers, and names only the proxy can resolve, still go through.
	for _, target := range []string{"http://public.test/", "http://unknown.test/"} {
		if err := get(target); err != nil {
			t.Fatalf("%s: unexpected error: %v", target, err)
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("proxy hits = %d, want 2", hits.Load())
	}
}

func TestCreateSafeHTTPClient_ProxyTargetCheckHonorsWhitelist(t *testing.T) {
	stubLookup(t, map[string][]string{"nas.test": {"192.168.1.20"}})
	proxy, hits := newRecordingProxy(t)

	client, err := CreateSafeHTTPClient(SafeHTTPClientOptions{
		ProxyURL:             proxy.URL,
		Timeout:              5 * time.Second,
		PrivateHostWhitelist: []string{"192.168.1.0/24"},
	})
	if err != nil {
		t.Fatalf("CreateSafeHTTPClient() error: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://nas.test/", nil)
	AllowConfiguredProxyFirstHop(req, client.Transport)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("expected whitelisted target to pass, got %v", err)
	}
	_ = resp.Body.Close()
	if hits.Load() != 1 {
		t.Fatalf("proxy hits = %d, want 1", hits.Load())
	}
}

func TestAllowConfiguredProxyFirstHop_ExemptsOnlyProxyHostPort(t *testing.T) {
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer proxyListener.Close()
	other, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer other.Close()

	client, err := CreateSafeHTTPClient(SafeHTTPClientOptions{
		ProxyURL: "http://" + proxyListener.Addr().String(),
		Timeout:  time.Second,
	})
	if err != nil {
		t.Fatalf("CreateSafeHTTPClient() error: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://93.184.216.34/", nil)
	AllowConfiguredProxyFirstHop(req, client.Transport)

	dial := NewSafeDialContext(&net.Dialer{Timeout: time.Second}, nil, nil)
	conn, err := dial(req.Context(), "tcp", proxyListener.Addr().String())
	if err != nil {
		t.Fatalf("expected the proxy address to be exempt, got %v", err)
	}
	conn.Close()

	if _, err := dial(req.Context(), "tcp", other.Addr().String()); err == nil ||
		!strings.Contains(err.Error(), "blocked private or local target") {
		t.Fatalf("expected another port on the proxy host to stay blocked, got %v", err)
	}
}

func TestProxyDialAddr_DefaultPorts(t *testing.T) {
	tests := map[string]string{
		"http://Proxy.Example.:3128": "proxy.example:3128",
		"http://proxy.example":       "proxy.example:80",
		"https://proxy.example":      "proxy.example:443",
		"socks5://[::1]":             "[::1]:1080",
	}
	for raw, want := range tests {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("url.Parse(%q): %v", raw, err)
		}
		if got := proxyDialAddr(u); got != want {
			t.Errorf("proxyDialAddr(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestIsObviousPrivateHost_LegacyAndZonedLiterals(t *testing.T) {
	tests := map[string]bool{
		"127.1":           true,
		"2130706433":      true,
		"0x7f.0.0.1":      true,
		"0177.0.0.1":      true,
		"10.1":            true,
		"134744072":       false, // 8.8.8.8
		"fe80::1%eth0":    true,
		"example.com":     false,
		"1e100.net":       false,
		"localhost.":      true,
		"api.localhost":   true,
		"256.256.256.256": false, // not an address in any form
	}
	for host, want := range tests {
		if got := IsObviousPrivateHost(host, nil, nil); got != want {
			t.Errorf("IsObviousPrivateHost(%q) = %v, want %v", host, got, want)
		}
	}
}

func TestValidateSafeHTTPURL_BlocksLoopback(t *testing.T) {
	err := ValidateSafeHTTPURL("http://127.0.0.1:8080/file", nil, nil)
	if err == nil {
		t.Fatal("expected loopback URL to be blocked")
	}
	if !strings.Contains(err.Error(), "private or local network hosts") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateSafeHTTPURL_AllowsWhitelistedPrivateHost(t *testing.T) {
	whitelist, err := NewPrivateHostWhitelist([]string{"127.0.0.1"})
	if err != nil {
		t.Fatalf("NewPrivateHostWhitelist() error: %v", err)
	}

	err = ValidateSafeHTTPURL("http://127.0.0.1:8080/file", whitelist, nil)
	if err != nil {
		t.Fatalf("expected whitelisted private host to pass, got %v", err)
	}
}

func TestNewSafeDialContext_BlocksPrivateDNSResolutionWithoutWhitelist(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on loopback: %v", err)
	}
	defer listener.Close()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split listener address: %v", err)
	}

	dialContext := NewSafeDialContext(&net.Dialer{Timeout: time.Second}, nil, nil)
	_, err = dialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err == nil {
		t.Fatal("expected localhost DNS resolution to be blocked without whitelist")
	}
	if !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "whitelisted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewSafeDialContext_AllowsWhitelistedPrivateDNSResolution(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen on loopback: %v", err)
	}
	defer listener.Close()

	accepted := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		conn.Close()
		accepted <- struct{}{}
	}()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("failed to split listener address: %v", err)
	}

	whitelist, err := NewPrivateHostWhitelist([]string{"127.0.0.0/8"})
	if err != nil {
		t.Fatalf("failed to parse whitelist: %v", err)
	}

	dialContext := NewSafeDialContext(&net.Dialer{Timeout: time.Second}, whitelist, nil)
	conn, err := dialContext(context.Background(), "tcp", net.JoinHostPort("localhost", port))
	if err != nil {
		t.Fatalf("expected localhost DNS resolution to succeed with whitelist, got %v", err)
	}
	conn.Close()

	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("expected localhost listener to accept a connection")
	}
}

func TestIsPrivateOrRestrictedIP_Table(t *testing.T) {
	tests := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true},
		{"100.64.0.1", true},
		{"198.18.0.1", true},
		{"198.20.0.1", false},
		{"0.0.0.0", true},
		{"8.8.8.8", false},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"fc00::1", true},
		{"2002:7f00:0001::1", true},
		{"2002:0801:0101::1", false},
		{"2001:db8:1234::5efe:127.0.0.1", true},
		{"2001:db8:1234::5efe:10.0.0.1", true},
		{"2001:db8:1234::5efe:8.8.8.8", false},
		{"2001:db8:1234:0:0200:5efe:127.0.0.1", true},
		{"2001:db8:1234:0:0200:5efe:10.0.0.1", true},
		{"2001:db8:1234:0:0200:5efe:8.8.8.8", false},
		{"2001:0000:4136:e378:8000:63bf:f5ff:fffe", true},
		{"2607:f8b0:4004:800::200e", false},
		// NAT64 well-known prefix decodes the embedded IPv4 target.
		{"64:ff9b::7f00:1", true},
		{"64:ff9b::a00:1", true},
		{"64:ff9b::808:808", false},
		// NAT64 local-use prefix is never public.
		{"64:ff9b:1::1", true},
		{"64:ff9b:1:abcd::808:808", true},
		// Deprecated IPv4-compatible addresses.
		{"::8.8.8.8", true},
		{"::127.0.0.1", true},
		{"192.0.0.1", true},
		{"192.0.0.170", true},
		{"192.0.1.1", false},
		{"240.0.0.1", true},
		{"255.255.255.255", true},
		{"223.255.255.1", false},
		{"fec0::1", true},
		{"feff::1", true},
		{"2002:c000:0001::1", true},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("failed to parse IP: %s", tt.ip)
			}
			got := IsPrivateOrRestrictedIP(ip)
			if got != tt.blocked {
				t.Fatalf("IsPrivateOrRestrictedIP(%s) = %v, want %v", tt.ip, got, tt.blocked)
			}
		})
	}
}

func TestDownloadFile_DefaultAllowsLoopbackURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("local download"))
	}))
	defer server.Close()

	path := DownloadFile(server.URL, "file.txt", DownloadOptions{
		LoggerPrefix: "test",
		Timeout:      5 * time.Second,
	})
	if path == "" {
		t.Fatal("expected default DownloadFile to allow loopback URL")
	}
	defer os.Remove(path)
}

func TestDownloadFile_BlockPrivateTargetsBlocksRedirectToLoopback(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("secret"))
	}))
	defer target.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer proxy.Close()

	path := DownloadFile("http://example.com/file.txt", "file.txt", DownloadOptions{
		LoggerPrefix:        "test",
		Timeout:             5 * time.Second,
		ProxyURL:            proxy.URL,
		BlockPrivateTargets: true,
	})
	if path != "" {
		t.Fatalf("expected safe DownloadFile to block redirect to loopback, got %q", path)
	}
}
