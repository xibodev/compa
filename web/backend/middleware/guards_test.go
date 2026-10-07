package middleware

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestHostAllowlistAcceptsOnlyThisComputersNames(t *testing.T) {
	h := HostAllowlist(HostAllowlistConfig{
		ListenHosts:  []string{"myserver.lan,0.0.0.0", "::"},
		AllowedHosts: []string{"compa.example.com"},
		LocalAddrs: func() []net.IP {
			return []net.IP{net.ParseIP("192.168.1.20"), net.ParseIP("fe80::1")}
		},
	}, okHandler())

	for _, tc := range []struct {
		host string
		want int
	}{
		{"localhost:18800", http.StatusOK},
		{"localhost:5173", http.StatusOK},
		{"LOCALHOST", http.StatusOK},
		{"127.0.0.1:18800", http.StatusOK},
		{"127.0.0.2", http.StatusOK},
		{"[::1]:18800", http.StatusOK},
		{"192.168.1.20:18800", http.StatusOK},
		{"[fe80::1%25eth0]:18800", http.StatusOK},
		{"myserver.lan:18800", http.StatusOK},
		{"compa.example.com", http.StatusOK},
		{"compa.example.com.:443", http.StatusOK},
		// A rebinding page keeps its own name.
		{"evil.example:18800", http.StatusMisdirectedRequest},
		{"192.168.1.99:18800", http.StatusMisdirectedRequest},
		{"0.0.0.0:18800", http.StatusMisdirectedRequest},
		{"localhost.evil.example", http.StatusMisdirectedRequest},
	} {
		for _, path := range []string{"/api/auth/setup", "/"} {
			req := httptest.NewRequest(http.MethodPost, path, nil)
			req.Host = tc.host
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("Host %q %s = %d, want %d", tc.host, path, rec.Code, tc.want)
			}
		}
	}
}

func TestHostAllowlistRereadsAddressesForANewOne(t *testing.T) {
	addrs := []net.IP{net.ParseIP("10.0.0.5")}
	h := HostAllowlist(HostAllowlistConfig{LocalAddrs: func() []net.IP { return addrs }}, okHandler())
	allow := h

	serve := func(host string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		allow.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := serve("10.0.0.5:18800"); code != http.StatusOK {
		t.Fatalf("known address = %d", code)
	}
	// DHCP hands out a new address; it is seen once the refresh interval
	// passed, without a restart.
	addrs = []net.IP{net.ParseIP("10.0.0.6")}
	a := newHostAllowlist(HostAllowlistConfig{LocalAddrs: func() []net.IP { return addrs }})
	now := time.Now()
	a.now = func() time.Time { return now }
	if !a.allows("10.0.0.6") {
		t.Fatal("a new address was refused on first sight")
	}
	addrs = []net.IP{net.ParseIP("10.0.0.7")}
	if a.allows("10.0.0.7") {
		t.Fatal("addresses were re-read before the refresh interval")
	}
	now = now.Add(hostAddrRefreshInterval + time.Second)
	if !a.allows("10.0.0.7") {
		t.Fatal("addresses were not re-read after the refresh interval")
	}
}

func TestSameOriginGuard(t *testing.T) {
	h := SameOriginGuard(okHandler())

	for _, tc := range []struct {
		name, method, path, origin, site string
		want                             int
	}{
		{"same origin write", http.MethodPost, "/api/config", "http://127.0.0.1:18800", "", http.StatusOK},
		{"foreign origin write", http.MethodPost, "/api/config", "http://localhost:9999", "", http.StatusForbidden},
		{"other port of the same host", http.MethodPut, "/api/config", "http://127.0.0.1:9999", "", http.StatusForbidden},
		{"other scheme", http.MethodDelete, "/api/sessions/x", "https://127.0.0.1:18800", "", http.StatusForbidden},
		{"null origin", http.MethodPatch, "/api/config", "null", "", http.StatusForbidden},
		{"fetch metadata only, same origin", http.MethodPost, "/api/config", "", "same-origin", http.StatusOK},
		{"fetch metadata only, same site", http.MethodPost, "/api/config", "", "same-site", http.StatusForbidden},
		{"fetch metadata only, cross site", http.MethodPost, "/api/auth/setup", "", "cross-site", http.StatusForbidden},
		{"no browser headers", http.MethodPost, "/api/config", "", "", http.StatusOK},
		{"reads pass", http.MethodGet, "/api/config", "http://evil.example", "cross-site", http.StatusOK},
		{"websocket from a foreign page", http.MethodGet, "/web/ws", "http://evil.example", "", http.StatusForbidden},
		{"websocket from the dashboard", http.MethodGet, "/web/ws", "http://127.0.0.1:18800", "", http.StatusOK},
		{"pages are not checked", http.MethodPost, "/launcher-login", "http://evil.example", "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Host = "127.0.0.1:18800"
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestSameOriginRequestIgnoresDefaultPorts(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	req.Host = "compa.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://compa.example.com:443")
	if !SameOriginRequest(req) {
		t.Fatal("the default port made a same-origin request look foreign")
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := SecurityHeaders(SecurityHeadersConfig{ScriptHashes: []string{"'sha256-abc='"}}, okHandler())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"script-src 'self' 'sha256-abc='",
		"frame-ancestors 'none'",
		"object-src 'none'",
		"img-src 'self' data: blob: https:",
		"connect-src 'self' ws: wss:",
	} {
		if !strings.Contains(csp, want) {
			t.Errorf("Content-Security-Policy %q lacks %q", csp, want)
		}
	}
	if strings.Contains(csp, "'unsafe-eval'") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("Content-Security-Policy allows inline or eval scripts: %q", csp)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "" {
		t.Errorf("page Cache-Control = %q, want none", got)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("API Cache-Control = %q, want no-store", got)
	}
}

func TestAPIBodyLimit(t *testing.T) {
	var readErr error
	h := APIBodyLimit(16, map[string]int64{"/api/skills/import": 64}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 128)
		_, readErr = r.Body.Read(buf)
		for readErr == nil {
			_, readErr = r.Body.Read(buf)
		}
		w.WriteHeader(http.StatusOK)
	}))

	body := strings.Repeat("x", 32)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("declared oversize body = %d, want 413", rec.Code)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(body))
	req.ContentLength = -1
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if readErr == nil || !strings.Contains(readErr.Error(), "too large") {
		t.Fatalf("undeclared oversize body read error = %v, want a too-large error", readErr)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/skills/import", strings.NewReader(body)))
	if rec.Code != http.StatusOK || readErr == nil || strings.Contains(readErr.Error(), "too large") {
		t.Fatalf("route with its own limit = %d, %v", rec.Code, readErr)
	}
}

func TestRecovererReraisesAbortHandler(t *testing.T) {
	h := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if err := recover(); err != http.ErrAbortHandler {
			t.Fatalf("recovered %v, want http.ErrAbortHandler re-panicked", err)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/web/media/x", nil))
	t.Fatal("ErrAbortHandler was swallowed")
}

func TestRecovererWritesNothingAfterHeaders(t *testing.T) {
	h := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "partial" {
		t.Fatalf("response = %d %q, want the partial answer untouched", rec.Code, rec.Body.String())
	}

	h = Recoverer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom") }))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/x", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("response = %d %q, want a 500 JSON error", rec.Code, rec.Body.String())
	}
}

func TestLauncherDashboardSessionRotationSignsEveryoneOut(t *testing.T) {
	session, err := NewLauncherDashboardSession()
	if err != nil {
		t.Fatal(err)
	}
	old := session.Token()
	h := LauncherDashboardAuth(LauncherDashboardAuthConfig{Session: session}, okHandler())

	serve := func(cookie string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/config", nil)
		req.AddCookie(&http.Cookie{Name: LauncherDashboardCookieName, Value: cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := serve(old); code != http.StatusOK {
		t.Fatalf("current cookie = %d", code)
	}
	fresh, err := session.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if fresh == old {
		t.Fatal("Rotate() kept the token")
	}
	if code := serve(old); code != http.StatusUnauthorized {
		t.Fatalf("cookie from before the rotation = %d, want 401", code)
	}
	if code := serve(fresh); code != http.StatusOK {
		t.Fatalf("cookie after the rotation = %d", code)
	}
}

func TestLauncherDashboardSetupURLPath(t *testing.T) {
	if got := LauncherDashboardSetupURLPath(""); got != "/launcher-setup" {
		t.Fatalf("LauncherDashboardSetupURLPath(\"\") = %q", got)
	}
	if got := LauncherDashboardSetupURLPath("a+b/c"); got != "/launcher-setup?token=a%2Bb%2Fc" {
		t.Fatalf("LauncherDashboardSetupURLPath() = %q", got)
	}
}

// The CSP admits the dashboard's theme script by its hash; a page whose
// script changed would run nothing before first paint.
func TestInlineScriptHashesMatchTheDashboardPage(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "frontend", "index.html"))
	if err != nil {
		t.Skipf("no frontend page: %v", err)
	}
	// A checkout with CRLF line endings would hash differently; the page is
	// pinned to LF in .gitattributes, as browsers hash what is served.
	page = bytes.ReplaceAll(page, []byte("\r\n"), []byte("\n"))
	hashes := InlineScriptHashes(page)
	if len(hashes) != 1 || hashes[0] != DashboardThemeScriptHash {
		t.Fatalf("inline script hashes = %v, want only DashboardThemeScriptHash %s; update the constant", hashes, DashboardThemeScriptHash)
	}
	if got := InlineScriptHashes([]byte(`<script src="/a.js"></script><script type="module">x()</script>`)); len(got) != 1 {
		t.Fatalf("hashes = %v, want only the inline module script", got)
	}
}

// A slow body is cut off, and a handler that outlives the body read keeps
// its connection: the read deadline is lifted once the body is read.
func TestAPIBodyLimitLiftsTheReadDeadlineAfterTheBody(t *testing.T) {
	handler := APIBodyLimit(DefaultAPIBodyLimit, nil, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Errorf("read body: %v", err)
		}
		select {
		case <-r.Context().Done():
			t.Error("the request context ended after the body was read")
		case <-time.After(50 * time.Millisecond):
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server := httptest.NewServer(handler)
	defer server.Close()
	response, err := http.Post(server.URL+"/api/x", "application/json", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", response.StatusCode)
	}
}
