package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/web/backend/middleware"
)

func TestLoginRateLimiterForgetsTheLeastRecentClient(t *testing.T) {
	l := newLoginRateLimiter()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return t0 }

	for i := range loginLimiterMaxClients + 50 {
		// Stay under the global limit by moving the clock past the window.
		l.now = func() time.Time { return t0.Add(time.Duration(i) * loginAttemptWindow) }
		if !l.allow(fmt.Sprintf("10.0.%d.%d", i/256, i%256)) {
			t.Fatalf("client %d refused", i)
		}
	}
	if len(l.clients) != loginLimiterMaxClients || l.order.Len() != loginLimiterMaxClients {
		t.Fatalf("tracked clients = %d (%d in order), want %d", len(l.clients), l.order.Len(), loginLimiterMaxClients)
	}
	if _, ok := l.clients["10.0.0.0"]; ok {
		t.Fatal("the least recently seen client was kept")
	}
}

func TestLoginRateLimiterHasAGlobalLimit(t *testing.T) {
	l := newLoginRateLimiter()
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return t0 }

	for i := range loginAttemptsGlobal {
		if !l.allow(fmt.Sprintf("192.0.2.%d", i%250)) {
			t.Fatalf("attempt %d refused before the global limit", i)
		}
	}
	if l.allow("198.51.100.1") {
		t.Fatal("a fresh client passed the global limit")
	}
	l.now = func() time.Time { return t0.Add(loginAttemptWindow + time.Second) }
	if !l.allow("198.51.100.1") {
		t.Fatal("the global limit outlived its window")
	}
}

func TestLoginLimiterCountsTheClientBehindATrustedProxy(t *testing.T) {
	resolver, err := middleware.NewClientIPResolver([]string{"10.0.0.0/8"})
	if err != nil {
		t.Fatalf("NewClientIPResolver() error = %v", err)
	}
	request := func(remote, forwarded string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		r.RemoteAddr = remote
		if forwarded != "" {
			r.Header.Set("X-Forwarded-For", forwarded)
		}
		return r
	}

	for _, tc := range []struct {
		name, remote, forwarded, want string
	}{
		{"trusted proxy", "10.0.0.5:4000", "203.0.113.7, 10.0.0.9", "203.0.113.7"},
		{"spoofed by a direct client", "198.51.100.2:4000", "203.0.113.7", "198.51.100.2"},
		{"proxy without the header", "10.0.0.5:4000", "", "10.0.0.5"},
	} {
		if got := clientIPForLimiter(request(tc.remote, tc.forwarded), resolver); got != tc.want {
			t.Errorf("%s: client = %q, want %q", tc.name, got, tc.want)
		}
	}

	// Two clients behind one proxy no longer share one budget.
	l := newLoginRateLimiter()
	for range loginAttemptsPerIP {
		l.allow(clientIPForLimiter(request("10.0.0.5:4000", "203.0.113.7"), resolver))
	}
	if !l.allow(clientIPForLimiter(request("10.0.0.5:4000", "203.0.113.8"), resolver)) {
		t.Fatal("another client behind the proxy was refused")
	}
	if l.allow(clientIPForLimiter(request("10.0.0.5:4000", "203.0.113.7"), resolver)) {
		t.Fatal("the client over its limit was let through")
	}
}
