package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v4/internal/module"
)

// The bug this exists for: DELETE /api/modules/%2E%2E reached module.Remove
// with "..", which deleted the whole Compa home. Browsers normalise the path;
// other clients do not, and the router decodes the segment after matching.
func TestModuleRoutesRefuseAnIDThatIsNotAModule(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	keep := filepath.Join(home, "config.json")
	if err := os.WriteFile(keep, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(module.ModulesDir(home), "keep")
	if err := os.MkdirAll(installed, 0o755); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	NewHandler(keep).RegisterRoutes(mux)

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodDelete, "/api/modules/%2E%2E", nil),
		httptest.NewRequest(http.MethodDelete, "/api/modules/%2E", nil),
		httptest.NewRequest(http.MethodDelete, "/api/modules/..%5Ckeep", nil),
		httptest.NewRequest(http.MethodPost, "/api/modules/%2E%2E/enabled", strings.NewReader(`{"enabled":false}`)),
		httptest.NewRequest(http.MethodPost, "/api/modules/%2E%2E/invoke", strings.NewReader(`{"capability":"x"}`)),
		httptest.NewRequest(http.MethodGet, "/api/modules/artifact?module=..&root=workspace&path=x", nil),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s %s: status %d, want 400: %s", req.Method, req.URL, rec.Code, rec.Body.String())
		}
	}
	for _, p := range []string{keep, installed} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a refused request deleted %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "workspace")); !os.IsNotExist(err) {
		t.Fatalf("a refused request created the workspace: %v", err)
	}
}
