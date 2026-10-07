package middleware

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// site.webmanifest is served WITHOUT a session, because the login page itself
// references it. So every icon the manifest names must also be public.
//
// The bug this exists for: two of the four manifest icons
// (web-app-manifest-192x192.png, -512x512.png) were missing from the public
// allow-list and returned 302 to /launcher-login. An install prompt shown
// before sign-in resolved its icons to an HTML page.
//
// This is the "duplicated decisions" family again -- the manifest lists icons,
// the allow-list lists public paths, and nothing checked they agree. Verified
// against the running server with curl before writing this: the two PNGs
// answered 302 while apple-touch-icon.png answered 200.
func TestEveryManifestIconIsPublic(t *testing.T) {
	path := filepath.Join("..", "..", "frontend", "public", "site.webmanifest")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no manifest at %s: %v", path, err)
	}

	var manifest struct {
		Icons []struct {
			Src string `json:"src"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		t.Fatalf("site.webmanifest is not valid JSON: %v", err)
	}
	if len(manifest.Icons) == 0 {
		t.Fatal("the manifest declares no icons, so this test cannot detect" +
			" one becoming unreachable")
	}

	for _, icon := range manifest.Icons {
		src := strings.TrimSpace(icon.Src)
		if src == "" || !strings.HasPrefix(src, "/") {
			continue // a remote or relative icon is not ours to allow-list
		}
		if !isPublicLauncherDashboardStatic("GET", src) {
			t.Errorf("the manifest names %s but it is not public, so an"+
				" unauthenticated install prompt gets a redirect to the login"+
				" page instead of an icon", src)
		}
	}
}

// The manifest itself has to be public for any of the above to matter.
func TestTheManifestItselfIsPublic(t *testing.T) {
	if !isPublicLauncherDashboardStatic("GET", "/site.webmanifest") {
		t.Fatal("site.webmanifest requires a session, so the login page cannot" +
			" load it at all")
	}
}

// Every path the allow-list promises must actually exist in the directory that
// is EMBEDDED INTO THE BINARY.
//
// The other half of the same bug: /robots.txt was allow-listed and returned
// 404, because no such file was ever shipped. A public path with nothing behind
// it is a promise the server cannot keep -- for robots.txt specifically, a
// crawler reaching an exposed instance got a 404 and applied its own default,
// which is to crawl.
//
// This checks web/backend/dist, NOT web/frontend/public, and the distinction is
// the whole point. There are two dist directories: `pnpm build` writes
// web/frontend/dist, while the binary embeds web/backend/dist via
// `//go:embed all:dist` -- only `pnpm build:backend` puts a file there.
//
// An earlier version of this test read frontend/public and passed while the
// running server still answered 404, because a file existing in source proves
// nothing about what shipped. Verified by probing a launcher built from the new
// assets: robots.txt was still missing, the two manifest icons were fixed.
func TestEveryPublicBrandPathIsActuallyEmbedded(t *testing.T) {
	dir := filepath.Join("..", "dist")
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		t.Skipf("no embedded dist at %s (run `pnpm build:backend`): %v", dir, err)
	}

	// The paths the allow-list serves from disk, as opposed to SPA routes and
	// the bundled /assets/ prefix.
	for _, p := range []string{
		"/compa-mark.svg", "/compa-logo.svg",
		"/favicon.ico", "/favicon.svg", "/favicon-96x96.png",
		"/apple-touch-icon.png", "/site.webmanifest",
		"/web-app-manifest-192x192.png", "/web-app-manifest-512x512.png",
		"/robots.txt",
	} {
		if !isPublicLauncherDashboardStatic("GET", p) {
			t.Errorf("%s is expected to be public and is not", p)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, strings.TrimPrefix(p, "/"))); err != nil {
			t.Errorf("%s is served without a session but no file backs it in the"+
				" EMBEDDED dist, so the running server answers 404: %v", p, err)
		}
	}
}

// The manifest that ships must be the one whose icons were checked. If the two
// dist directories drift, the icon check above is validating a file the binary
// does not contain.
func TestTheEmbeddedManifestMatchesTheSourceManifest(t *testing.T) {
	embedded := filepath.Join("..", "dist", "site.webmanifest")
	source := filepath.Join("..", "..", "frontend", "public", "site.webmanifest")

	got, err := os.ReadFile(embedded)
	if err != nil {
		t.Skipf("no embedded manifest (run `pnpm build:backend`): %v", err)
	}
	want, err := os.ReadFile(source)
	if err != nil {
		t.Skipf("no source manifest: %v", err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Error("the embedded site.webmanifest differs from the source one, so" +
			" the binary ships different icons than the ones checked above --" +
			" run `pnpm build:backend`, not `pnpm build`")
	}
}

// Public brand assets are readable, never writable. A POST to one must not be
// treated as public just because the path is.
func TestBrandAssetsArePublicOnlyForReads(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		if isPublicLauncherDashboardStatic(method, "/favicon.ico") {
			t.Errorf("%s /favicon.ico was treated as a public request", method)
		}
	}
}
