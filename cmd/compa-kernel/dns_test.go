package main

import (
	"net"
	"os"
	"runtime"
	"testing"
)

// compa-kernel leaves name lookups to the system's resolver, so VPN and
// company DNS and local host names work. Only a Linux system without
// /etc/resolv.conf gets other DNS servers.
func TestKernelUsesTheSystemResolver(t *testing.T) {
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/etc/resolv.conf"); err != nil {
			t.Skip("this Linux system has no /etc/resolv.conf, so compa-kernel picks DNS servers itself")
		}
	}
	if net.DefaultResolver.Dial != nil || net.DefaultResolver.PreferGo {
		t.Fatal("compa-kernel replaced the system's resolver")
	}
}
