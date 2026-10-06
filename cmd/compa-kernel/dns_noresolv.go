//go:build linux

package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// On a Linux system without /etc/resolv.conf, such as Android, Go's resolver
// asks a DNS server on localhost, which isn't there. compa-kernel asks the
// servers in COMPA_DNS_SERVER instead (see dnsServers), in turn. Elsewhere
// the system's resolver stays in charge.
func init() {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return
	}

	servers := dnsServers(os.Getenv("COMPA_DNS_SERVER"))
	var idx uint64

	customResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			server := servers[atomic.AddUint64(&idx, 1)%uint64(len(servers))]
			return d.DialContext(ctx, "udp", server)
		},
	}

	net.DefaultResolver = customResolver

	// http.DefaultTransport dials with its own dialer, which needs the
	// resolver too.
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Resolver:  customResolver,
	}

	if tr, ok := http.DefaultTransport.(*http.Transport); ok {
		tr.DialContext = dialer.DialContext
	}
}

// dnsServers reads the servers of value, separated by ";", such as
// "9.9.9.9;1.1.1.1:53"; a server without a port is on port 53. A value that
// names none gives 8.8.8.8 and 1.1.1.1.
func dnsServers(value string) []string {
	var servers []string
	for _, s := range strings.Split(value, ";") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(s); err != nil {
			s = net.JoinHostPort(s, "53")
		}
		servers = append(servers, s)
	}
	if len(servers) == 0 {
		return []string{"8.8.8.8:53", "1.1.1.1:53"}
	}
	return servers
}
