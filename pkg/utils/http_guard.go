package utils

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

type SafeHTTPClientOptions struct {
	ProxyURL             string
	Timeout              time.Duration
	PrivateHostWhitelist []string
	AllowPrivateHosts    func() bool
	MaxRedirects         int
}

type PrivateHostWhitelist struct {
	exact map[string]struct{}
	cidrs []*net.IPNet
}

// allowedFirstHopKey carries the proxy host:port a request may dial even
// though it is private (a local proxy is the common case).
type allowedFirstHopKey struct{}

// proxyProbeKey marks the request AllowConfiguredProxyFirstHop uses to ask
// the transport for its proxy, so the probe skips the target lookup.
type proxyProbeKey struct{}

// lookupIPAddr resolves target hosts; tests replace it.
var lookupIPAddr = net.DefaultResolver.LookupIPAddr

func NewPrivateHostWhitelist(entries []string) (*PrivateHostWhitelist, error) {
	if len(entries) == 0 {
		return nil, nil
	}

	whitelist := &PrivateHostWhitelist{
		exact: make(map[string]struct{}),
		cidrs: make([]*net.IPNet, 0, len(entries)),
	}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			whitelist.exact[normalizeWhitelistIP(ip).String()] = struct{}{}
			continue
		}
		_, network, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid entry %q: expected IP or CIDR", entry)
		}
		whitelist.cidrs = append(whitelist.cidrs, network)
	}

	if len(whitelist.exact) == 0 && len(whitelist.cidrs) == 0 {
		return nil, nil
	}
	return whitelist, nil
}

func (w *PrivateHostWhitelist) Contains(ip net.IP) bool {
	if w == nil || ip == nil {
		return false
	}

	normalized := normalizeWhitelistIP(ip)
	if _, ok := w.exact[normalized.String()]; ok {
		return true
	}
	for _, network := range w.cidrs {
		if network.Contains(normalized) {
			return true
		}
	}
	return false
}

func CreateSafeHTTPClient(opts SafeHTTPClientOptions) (*http.Client, error) {
	client, err := CreateHTTPClient(opts.ProxyURL, opts.Timeout)
	if err != nil {
		return nil, err
	}

	whitelist, err := NewPrivateHostWhitelist(opts.PrivateHostWhitelist)
	if err != nil {
		return nil, err
	}

	transport, ok := client.Transport.(*http.Transport)
	if ok {
		dialer := &net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}
		transport.DialContext = NewSafeDialContext(dialer, whitelist, opts.AllowPrivateHosts)
		if transport.Proxy != nil {
			transport.Proxy = guardProxiedTarget(transport.Proxy, whitelist, opts.AllowPrivateHosts)
		}
	}

	maxRedirects := opts.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = 10
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if IsObviousPrivateHost(req.URL.Hostname(), whitelist, opts.AllowPrivateHosts) {
			return fmt.Errorf("redirect target is private or local network host")
		}
		AllowConfiguredProxyFirstHop(req, client.Transport)
		return nil
	}

	return client, nil
}

func ValidateSafeHTTPURL(urlStr string, whitelist *PrivateHostWhitelist, allowPrivateHosts func() bool) error {
	parsedURL, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("only http/https URLs are allowed")
	}
	if parsedURL.Host == "" {
		return fmt.Errorf("missing domain in URL")
	}
	if IsObviousPrivateHost(parsedURL.Hostname(), whitelist, allowPrivateHosts) {
		return fmt.Errorf("fetching private or local network hosts is not allowed")
	}
	return nil
}

func NewSafeDialContext(
	dialer *net.Dialer,
	whitelist *PrivateHostWhitelist,
	allowPrivateHosts func() bool,
) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if allowPrivateHosts != nil && allowPrivateHosts() {
			return dialer.DialContext(ctx, network, address)
		}

		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid target address %q: %w", address, err)
		}
		if host == "" {
			return nil, fmt.Errorf("empty target host")
		}
		if isAllowedFirstHop(ctx, address) {
			return dialer.DialContext(ctx, network, address)
		}

		if ip := net.ParseIP(host); ip != nil {
			if shouldBlockPrivateIP(ip, whitelist) {
				return nil, fmt.Errorf("blocked private or local target: %s", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}

		ipAddrs, err := lookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve %s: %w", host, err)
		}

		attempted := 0
		var lastErr error
		for _, ipAddr := range ipAddrs {
			if shouldBlockPrivateIP(ipAddr.IP, whitelist) {
				continue
			}
			attempted++
			conn, err := dialer.DialContext(
				ctx,
				network,
				net.JoinHostPort(ipAddr.IP.String(), port),
			)
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}

		if attempted == 0 {
			return nil, fmt.Errorf(
				"all resolved addresses for %s are private, restricted, or not whitelisted",
				host,
			)
		}
		if lastErr != nil {
			return nil, fmt.Errorf(
				"failed connecting to public addresses for %s: %w",
				host,
				lastErr,
			)
		}
		return nil, fmt.Errorf("failed connecting to public addresses for %s", host)
	}
}

// AllowConfiguredProxyFirstHop lets req dial its configured proxy even when
// the proxy is private. The exemption is the proxy's host:port, so other
// ports on that host stay blocked.
func AllowConfiguredProxyFirstHop(req *http.Request, rt http.RoundTripper) {
	if req == nil {
		return
	}

	transport, ok := rt.(*http.Transport)
	if !ok || transport.Proxy == nil {
		return
	}

	probe := req.WithContext(context.WithValue(req.Context(), proxyProbeKey{}, true))
	proxyURL, err := transport.Proxy(probe)
	if err != nil || proxyURL == nil {
		return
	}

	addr := proxyDialAddr(proxyURL)
	if addr == "" {
		return
	}

	*req = *req.WithContext(context.WithValue(
		req.Context(),
		allowedFirstHopKey{},
		addr,
	))
}

// guardProxiedTarget applies the private-target policy to proxied requests.
// Only the proxy is dialed then, so the dial-time check never sees the
// target: it is resolved locally first and refused when any answer is
// private or special. A failed lookup is let through, since the proxy may
// resolve names the local resolver can't.
func guardProxiedTarget(
	proxy func(*http.Request) (*url.URL, error),
	whitelist *PrivateHostWhitelist,
	allowPrivateHosts func() bool,
) func(*http.Request) (*url.URL, error) {
	return func(req *http.Request) (*url.URL, error) {
		proxyURL, err := proxy(req)
		if err != nil || proxyURL == nil || req.URL == nil {
			return proxyURL, err
		}
		if probing, _ := req.Context().Value(proxyProbeKey{}).(bool); probing {
			return proxyURL, nil
		}
		if allowPrivateHosts != nil && allowPrivateHosts() {
			return proxyURL, nil
		}
		if err := checkProxiedTarget(req.Context(), req.URL.Hostname(), whitelist); err != nil {
			return nil, err
		}
		return proxyURL, nil
	}
}

func checkProxiedTarget(ctx context.Context, host string, whitelist *PrivateHostWhitelist) error {
	if IsObviousPrivateHost(host, whitelist, nil) {
		return fmt.Errorf("blocked private or local target: %s", host)
	}
	h := normalizeHostName(host)
	if parseHostIP(h) != nil {
		return nil
	}
	addrs, err := lookupIPAddr(ctx, asciiHost(h))
	if err != nil {
		return nil
	}
	for _, addr := range addrs {
		if shouldBlockPrivateIP(addr.IP, whitelist) {
			return fmt.Errorf("blocked private or local target: %s resolves to %s", host, addr.IP)
		}
	}
	return nil
}

func IsObviousPrivateHost(
	host string,
	whitelist *PrivateHostWhitelist,
	allowPrivateHosts func() bool,
) bool {
	if allowPrivateHosts != nil && allowPrivateHosts() {
		return false
	}

	h := normalizeHostName(host)
	if h == "" {
		return true
	}

	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}

	if ip := parseHostIP(h); ip != nil {
		return shouldBlockPrivateIP(ip, whitelist)
	}

	return false
}

// parseHostIP parses a literal address the way other resolvers may: an IPv6
// zone is dropped and the legacy inet_aton forms ("127.1", "2130706433",
// "0x7f.0.0.1") are accepted, because a proxy would connect to them.
func parseHostIP(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	if i := strings.IndexByte(host, '%'); i > 0 {
		if ip := net.ParseIP(host[:i]); ip != nil {
			return ip
		}
	}
	return parseLegacyIPv4(host)
}

func parseLegacyIPv4(host string) net.IP {
	parts := strings.Split(host, ".")
	if len(parts) > 4 {
		return nil
	}
	vals := make([]uint64, len(parts))
	for i, part := range parts {
		base := 10
		switch {
		case len(part) > 2 && (part[:2] == "0x" || part[:2] == "0X"):
			base, part = 16, part[2:]
		case len(part) > 1 && part[0] == '0':
			base, part = 8, part[1:]
		}
		v, err := strconv.ParseUint(part, base, 32)
		if err != nil {
			return nil
		}
		vals[i] = v
	}
	// The last part fills the remaining bytes, as in inet_aton.
	n := len(vals)
	var addr uint64
	for _, v := range vals[:n-1] {
		if v > 0xff {
			return nil
		}
		addr = addr<<8 | v
	}
	shift := uint(8 * (5 - n))
	if vals[n-1] > 1<<shift-1 {
		return nil
	}
	addr = addr<<shift | vals[n-1]
	return net.IPv4(byte(addr>>24), byte(addr>>16), byte(addr>>8), byte(addr))
}

func IsPrivateOrRestrictedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}

	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 10 ||
			ip4[0] == 127 ||
			ip4[0] == 0 ||
			(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
			(ip4[0] == 192 && ip4[1] == 168) ||
			(ip4[0] == 169 && ip4[1] == 254) ||
			(ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127) ||
			(ip4[0] == 198 && ip4[1] >= 18 && ip4[1] <= 19) ||
			// IETF protocol assignments (DS-Lite, NAT64 discovery, ...).
			(ip4[0] == 192 && ip4[1] == 0 && ip4[2] == 0) ||
			// Reserved 240.0.0.0/4, which includes the broadcast address.
			ip4[0] >= 240 {
			return true
		}
		return false
	}

	if len(ip) == net.IPv6len {
		// Unique local fc00::/7 and the deprecated site-local fec0::/10.
		if (ip[0]&0xfe) == 0xfc || (ip[0] == 0xfe && ip[1]&0xc0 == 0xc0) {
			return true
		}
		// Deprecated IPv4-compatible addresses (::a.b.c.d).
		if allZero(ip[:12]) {
			return true
		}
		// NAT64: the well-known prefix 64:ff9b::/96 carries the IPv4 target
		// in its last 32 bits; the local-use 64:ff9b:1::/48 is never public.
		if ip[0] == 0x00 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b {
			if allZero(ip[4:12]) {
				return IsPrivateOrRestrictedIP(net.IPv4(ip[12], ip[13], ip[14], ip[15]))
			}
			if ip[4] == 0x00 && ip[5] == 0x01 {
				return true
			}
		}
		if ip[0] == 0x20 && ip[1] == 0x02 {
			embedded := net.IPv4(ip[2], ip[3], ip[4], ip[5])
			return IsPrivateOrRestrictedIP(embedded)
		}
		if ip[0] == 0x20 && ip[1] == 0x01 && ip[2] == 0x00 && ip[3] == 0x00 {
			client := net.IPv4(ip[12]^0xff, ip[13]^0xff, ip[14]^0xff, ip[15]^0xff)
			return IsPrivateOrRestrictedIP(client)
		}
		// ISATAP interface identifiers embed an IPv4 address behind either
		// 00:00:5e:fe or 02:00:5e:fe.
		if ((ip[8] == 0x00 && ip[9] == 0x00) || (ip[8] == 0x02 && ip[9] == 0x00)) &&
			ip[10] == 0x5e && ip[11] == 0xfe {
			embedded := net.IPv4(ip[12], ip[13], ip[14], ip[15])
			return IsPrivateOrRestrictedIP(embedded)
		}
	}

	return false
}

func isAllowedFirstHop(ctx context.Context, address string) bool {
	allowed, ok := ctx.Value(allowedFirstHopKey{}).(string)
	if !ok || allowed == "" {
		return false
	}
	return allowed == normalizeHostPort(address)
}

// proxyDialAddr mirrors the address http.Transport dials for a proxy URL.
func proxyDialAddr(proxyURL *url.URL) string {
	host := proxyURL.Hostname()
	if host == "" {
		return ""
	}
	port := proxyURL.Port()
	if port == "" {
		switch strings.ToLower(proxyURL.Scheme) {
		case "http":
			port = "80"
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			return ""
		}
	}
	return normalizeHostPort(net.JoinHostPort(asciiHost(host), port))
}

func normalizeHostPort(address string) string {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	host = normalizeHostName(host)
	if host == "" || port == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

func normalizeHostName(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	return strings.TrimSuffix(host, ".")
}

// asciiHost converts an internationalized name the way net/http does
// before it dials or resolves.
func asciiHost(host string) string {
	for i := 0; i < len(host); i++ {
		if host[i] >= 0x80 {
			if ascii, err := idna.Lookup.ToASCII(host); err == nil {
				return ascii
			}
			return host
		}
	}
	return host
}

func allZero(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}

func normalizeWhitelistIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	return ip
}

func shouldBlockPrivateIP(ip net.IP, whitelist *PrivateHostWhitelist) bool {
	return IsPrivateOrRestrictedIP(ip) && !whitelist.Contains(ip)
}
