package middleware

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// SameOriginGuard refuses API writes (POST, PUT, PATCH, DELETE) and the web
// chat WebSocket handshake when a browser sent them from another origin. The
// session cookie is SameSite=Lax, which browsers also send from any other
// localhost port, so the cookie alone does not show that the dashboard asked.
// A request with neither Origin nor Sec-Fetch-Site comes from no browser, such
// as curl holding the cookie, and passes: its cookie is the gate.
func SameOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if originChecked(r) && !SameOriginRequest(r) {
			if canonicalAuthPath(r.URL.Path) == "/web/ws" {
				http.Error(w, "cross-origin request rejected", http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"cross-origin request rejected"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func originChecked(r *http.Request) bool {
	p := canonicalAuthPath(r.URL.Path)
	if p == "/web/ws" {
		return true
	}
	if !strings.HasPrefix(p, "/api/") {
		return false
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// SameOriginRequest reports whether r may act: a browser request must come
// from the page origin r itself names (scheme://host), shown by Origin or, when
// a browser omits that, by Sec-Fetch-Site same-origin. A request with neither
// header comes from no browser and passes.
func SameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
		return site == "" || site == "same-origin"
	}
	return originMatchesRequest(origin, r)
}

func originMatchesRequest(origin string, r *http.Request) bool {
	if strings.ContainsAny(origin, " \t\r\n") {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	scheme := RequestScheme(r)
	if !strings.EqualFold(u.Scheme, scheme) {
		return false
	}
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	return canonicalOriginHost(u.Host, scheme) == canonicalOriginHost(host, scheme)
}

// canonicalOriginHost lowercases host and drops the scheme's default port,
// which browsers leave out of Origin and Host alike.
func canonicalOriginHost(host, scheme string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	h, port, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		if strings.Contains(h, ":") {
			return "[" + h + "]"
		}
		return h
	}
	return host
}

// RequestScheme is the scheme the browser used: https behind TLS or a proxy
// that says so in X-Forwarded-Proto, else http.
func RequestScheme(r *http.Request) string {
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		if i := strings.IndexByte(proto, ','); i >= 0 {
			proto = proto[:i]
		}
		proto = strings.ToLower(strings.TrimSpace(proto))
		if proto == "http" || proto == "https" {
			return proto
		}
	}
	if r.TLS != nil {
		return "https"
	}
	if r.URL != nil && r.URL.Scheme != "" {
		return r.URL.Scheme
	}
	return "http"
}
