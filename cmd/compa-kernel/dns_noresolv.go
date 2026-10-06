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
// servers in COMPA_DNS_SERVER instead, separated by ";", such as
// "8.8.8.8:53;1.1.1.1:53" (the default), in turn. Elsewhere the system's
// resolver stays in charge.
func init() {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return
	}

	dnsEnv := os.Getenv("COMPA_DNS_SERVER")
	if dnsEnv == "" {
		dnsEnv = "8.8.8.8:53;1.1.1.1:53"
	}

	var dnsServers []string
	for _, s := range strings.Split(dnsEnv, ";") {
		s = strings.TrimSpace(s)
		if s != "" {
			// A server without a port is on port 53.
			if _, _, err := net.SplitHostPort(s); err != nil {
				s = s + ":53"
			}
			dnsServers = append(dnsServers, s)
		}
	}

	var idx uint64

	customResolver := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			server := dnsServers[atomic.AddUint64(&idx, 1)%uint64(len(dnsServers))]
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
