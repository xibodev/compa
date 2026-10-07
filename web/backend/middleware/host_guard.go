package middleware

import (
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// hostAddrRefreshInterval bounds how often an unknown address re-reads this
// computer's interface addresses, which change with DHCP, Wi-Fi and VPNs.
const hostAddrRefreshInterval = 10 * time.Second

// HostAllowlistConfig lists the Host names the dashboard answers to besides
// the ones it always accepts: loopback names, this computer's addresses and
// its host name.
type HostAllowlistConfig struct {
	// ListenHosts are the hosts the launcher was told to listen on (-host or
	// COMPA_LAUNCHER_HOST). Wildcards add nothing.
	ListenHosts []string
	// AllowedHosts are extra names, such as a reverse proxy's
	// (launcher-config allowed_hosts).
	AllowedHosts []string
	// LocalAddrs returns this computer's addresses. Nil reads the interfaces.
	LocalAddrs func() []net.IP
}

// HostAllowlist answers only requests whose Host names this computer. A page
// on another site that rebinds its DNS name to this computer's address still
// sends its own name as Host, so it is refused before it reaches any
// endpoint, the first-run setup included. Ports are ignored: the Vite dev
// server proxies from localhost:5173.
func HostAllowlist(cfg HostAllowlistConfig, next http.Handler) http.Handler {
	allow := newHostAllowlist(cfg)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if allow.allows(r.Host) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusMisdirectedRequest)
			_, _ = w.Write([]byte(`{"error":"unknown host"}`))
			return
		}
		http.Error(w, "unknown host", http.StatusMisdirectedRequest)
	})
}

type hostAllowlist struct {
	names      map[string]struct{}
	localAddrs func() []net.IP

	mu        sync.Mutex
	local     map[string]struct{}
	refreshed time.Time
	now       func() time.Time
}

func newHostAllowlist(cfg HostAllowlistConfig) *hostAllowlist {
	a := &hostAllowlist{
		names:      make(map[string]struct{}),
		localAddrs: cfg.LocalAddrs,
		now:        time.Now,
	}
	if a.localAddrs == nil {
		a.localAddrs = interfaceAddrs
	}
	for _, list := range [][]string{cfg.ListenHosts, cfg.AllowedHosts} {
		for _, raw := range list {
			for token := range strings.SplitSeq(raw, ",") {
				if host := normalizeHostName(token); host != "" && host != "*" && !isUnspecifiedHost(host) {
					a.names[host] = struct{}{}
				}
			}
		}
	}
	if hostname, err := os.Hostname(); err == nil {
		if host := normalizeHostName(hostname); host != "" {
			a.names[host] = struct{}{}
			if !strings.Contains(host, ".") {
				a.names[host+".local"] = struct{}{}
			}
		}
	}
	return a
}

func (a *hostAllowlist) allows(hostport string) bool {
	host := normalizeHostName(hostport)
	if host == "" {
		// HTTP/1.0 without Host: no browser sends that.
		return strings.TrimSpace(hostport) == ""
	}
	if host == "localhost" {
		return true
	}
	if _, ok := a.names[host]; ok {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	return a.isLocalAddr(ip.String())
}

func (a *hostAllowlist) isLocalAddr(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.local[ip]; ok {
		return true
	}
	if a.local != nil && a.now().Sub(a.refreshed) < hostAddrRefreshInterval {
		return false
	}
	a.local = make(map[string]struct{})
	for _, addr := range a.localAddrs() {
		a.local[addr.String()] = struct{}{}
	}
	a.refreshed = a.now()
	_, ok := a.local[ip]
	return ok
}

// normalizeHostName returns the host of a Host header or host list entry:
// lowercased, without port, brackets, IPv6 zone or trailing dot.
func normalizeHostName(raw string) string {
	host := strings.ToLower(strings.TrimSpace(raw))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.Trim(host, "[]"), ".")
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String()
	}
	return host
}

func isUnspecifiedHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func interfaceAddrs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		switch v := addr.(type) {
		case *net.IPNet:
			ips = append(ips, v.IP)
		case *net.IPAddr:
			ips = append(ips, v.IP)
		}
	}
	return ips
}
