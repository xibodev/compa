//go:build linux

package main

import (
	"reflect"
	"testing"
)

// COMPA_DNS_SERVER names the servers; a value that names none, such as ";",
// gives the default ones rather than none.
func TestDNSServers(t *testing.T) {
	for value, want := range map[string][]string{
		"":                           {"8.8.8.8:53", "1.1.1.1:53"},
		" ; ":                        {"8.8.8.8:53", "1.1.1.1:53"},
		"9.9.9.9":                    {"9.9.9.9:53"},
		"10.0.0.1:5353; 2001:db8::1": {"10.0.0.1:5353", "[2001:db8::1]:53"},
		"[2001:db8::2]":              {"[2001:db8::2]:53"},
	} {
		if got := dnsServers(value); !reflect.DeepEqual(got, want) {
			t.Errorf("dnsServers(%q) = %v, want %v", value, got, want)
		}
	}
}
