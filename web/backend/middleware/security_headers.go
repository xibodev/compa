package middleware

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"regexp"
	"strings"
)

// DashboardThemeScriptHash is the CSP hash of the inline script in
// web/frontend/index.html that applies the saved theme before first paint.
// The launcher hashes the inline scripts of the page it embeds at startup;
// this is the fallback when it cannot.
const DashboardThemeScriptHash = "'sha256-R49sAvhnDYMkfDhvWDBUrx9TOFbwiKRoQrWcjzGQFsI='"

// inlineScriptPattern matches a script element: its attributes and text.
var (
	inlineScriptPattern  = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`)
	scriptSrcAttrPattern = regexp.MustCompile(`(?i)\bsrc\s*=`)
)

// InlineScriptHashes returns the CSP sources ('sha256-...') of the inline
// scripts in an HTML page, the ones without src, in page order.
func InlineScriptHashes(html []byte) []string {
	var hashes []string
	for _, match := range inlineScriptPattern.FindAllSubmatch(html, -1) {
		if scriptSrcAttrPattern.Match(match[1]) || len(match[2]) == 0 {
			continue
		}
		sum := sha256.Sum256(match[2])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return hashes
}

// SecurityHeadersConfig configures SecurityHeaders.
type SecurityHeadersConfig struct {
	// ScriptHashes are the CSP sources ('sha256-...') of the inline scripts
	// the dashboard page carries. Empty uses DashboardThemeScriptHash.
	ScriptHashes []string
}

// ContentSecurityPolicy is the dashboard's CSP. Remote https images stay
// allowed because the owner chooses whether chat shows them (remote_images);
// connect-src admits the web chat WebSocket.
func ContentSecurityPolicy(scriptHashes []string) string {
	if len(scriptHashes) == 0 {
		scriptHashes = []string{DashboardThemeScriptHash}
	}
	return strings.Join([]string{
		"default-src 'self'",
		"img-src 'self' data: blob: https:",
		"media-src 'self' blob: data:",
		"connect-src 'self' ws: wss:",
		"style-src 'self' 'unsafe-inline'",
		"script-src 'self' " + strings.Join(scriptHashes, " "),
		"frame-ancestors 'none'",
		"object-src 'none'",
		"base-uri 'self'",
	}, "; ")
}

// SecurityHeaders sets the content security policy, nosniff, framing and
// referrer policies on every response, and keeps API answers out of caches.
// A handler serving other content (a downloaded file) replaces the CSP.
func SecurityHeaders(cfg SecurityHeadersConfig, next http.Handler) http.Handler {
	csp := ContentSecurityPolicy(cfg.ScriptHashes)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
