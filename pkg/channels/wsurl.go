package channels

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// CheckWebSocketURL refuses an unencrypted ws:// connection to a host outside
// this computer and its local network: a channel's token and every message
// would cross the internet in clear text. wss:// is accepted. setting names
// the URL in errors, such as "OneBot ws_url".
func CheckWebSocketURL(ctx context.Context, rawURL, setting string) error {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return fmt.Errorf("invalid %s: %w", setting, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "wss":
		return nil
	case "ws":
	default:
		return fmt.Errorf("%s must start with ws:// or wss://", setting)
	}

	host := u.Hostname()
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return fmt.Errorf("resolve the host of %s %q: %w", setting, host, err)
		}
		for _, addr := range addrs {
			ips = append(ips, addr.IP)
		}
	}
	for _, ip := range ips {
		if !isLocalNetworkIP(ip) {
			return fmt.Errorf(
				"%s %q uses unencrypted ws:// to a host outside this computer and its local network; use wss://",
				setting, rawURL,
			)
		}
	}
	return nil
}

// sharedAddressSpace is 100.64.0.0/10 (RFC 6598), used by carrier NAT and by
// overlay networks such as Tailscale; it is not reachable from the internet.
var sharedAddressSpace = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

func isLocalNetworkIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || sharedAddressSpace.Contains(ip)
}
