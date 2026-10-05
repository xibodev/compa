package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/xibodev/compa/internal/module"
	"github.com/xibodev/compa/internal/moduletools"
	"github.com/xibodev/compa/pkg/modproto"
)

// The Modules page decides a run by the approval policy (tools.approval) with
// origin web, as the agent's calls are decided: the list offers "Approve and
// run" when the policy asks, and invoke runs what the policy allows, what it
// asks about only once a person pressed "Approve and run", and nothing it
// denies or hides. A run a rule allows or a person approved carries the
// operator's approval; one the default allows does not.
func TestModuleInvokeFollowsTheApprovalPolicy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COMPA_HOME", home)
	installAPIFakeModule(t, home)
	configPath := filepath.Join(home, "config.json")
	mux := http.NewServeMux()
	NewHandler(configPath).RegisterRoutes(mux)

	var ran *modproto.Request
	original := moduleInvokeRunner
	moduleInvokeRunner = func(ctx context.Context, r *module.Runner, d *modproto.Descriptor, req *modproto.Request) (*module.Result, error) {
		ran = req
		return original(ctx, r, d, req)
	}
	t.Cleanup(func() { moduleInvokeRunner = original })

	const echo, unpriced = "fake.echo", "fake.estimate.unpriced"
	for _, tc := range []struct {
		name       string
		policy     string // tools.approval; empty is the default policy
		capability string
		requested  bool // the person pressed "Approve and run"
		needs      bool // the list offers "Approve and run"
		status     int
		approved   bool // the run carries the operator's approval
	}{
		{"the default allows a local capability", "", echo, false, false, http.StatusOK, false},
		{"the default asks about an unknown cost", "", unpriced, false, true, http.StatusForbidden, false},
		{"an ask approved on the page", "", unpriced, true, true, http.StatusOK, true},
		{"allow by default", `{"default":"allow","rules":[]}`, unpriced, true, false, http.StatusOK, false},
		{"a rule allows", `{"rules":[{"source":"module:fake","action":"allow"}]}`, unpriced, false, false, http.StatusOK, true},
		{"a rule asks", `{"rules":[{"tool":"fake__fake_echo","origin":["web"],"action":"ask"}]}`, echo, false, true, http.StatusForbidden, false},
		{"deny", `{"rules":[{"hints":["network"],"action":"deny"}]}`, unpriced, true, false, http.StatusForbidden, false},
		{"hide", `{"rules":[{"source":"module:*","action":"hide"}]}`, echo, true, false, http.StatusForbidden, false},
		{"a rule for another origin", `{"rules":[{"source":"module:fake","origin":["chat","cli"],"action":"deny"}]}`, echo, false, false, http.StatusOK, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeApprovalPolicy(t, configPath, tc.policy)
			if got := listedNeedsApproval(t, mux, tc.capability); got != tc.needs {
				t.Errorf("needs_approval = %v, want %v", got, tc.needs)
			}

			ran = nil
			rec := invokeFake(mux, tc.capability, tc.requested)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.status != http.StatusOK {
				if ran != nil {
					t.Fatal("a refused run reached the module")
				}
				if tc.needs && !strings.Contains(rec.Body.String(), "Approve and run") {
					t.Errorf("the refusal does not name the remedy: %s", rec.Body.String())
				}
				if !tc.needs && !strings.Contains(rec.Body.String(), "denied by the approval policy") {
					t.Errorf("the refusal does not say the policy denied it: %s", rec.Body.String())
				}
				return
			}
			if ran == nil {
				t.Fatal("an allowed run did not reach the module")
			}
			if _, carried := ran.Extra["consent"]; carried != tc.approved {
				t.Errorf("the run carries an approval: %v, want %v", carried, tc.approved)
			}
		})
	}

	t.Run("an undeclared capability", func(t *testing.T) {
		writeApprovalPolicy(t, configPath, `{"default":"allow","rules":[]}`)
		ran = nil
		rec := invokeFake(mux, "fake.undeclared", true)
		if rec.Code != http.StatusForbidden || ran != nil || !strings.Contains(rec.Body.String(), "declares no capability") {
			t.Fatalf("status = %d, ran = %v: %s", rec.Code, ran != nil, rec.Body.String())
		}
	})

	// A policy that cannot be read refuses every run rather than guessing.
	t.Run("an unreadable policy", func(t *testing.T) {
		writeApprovalPolicy(t, configPath, `{"default":"maybe"}`)
		ran = nil
		rec := invokeFake(mux, echo, true)
		if rec.Code != http.StatusInternalServerError || ran != nil || !strings.Contains(rec.Body.String(), "approval policy") {
			t.Fatalf("status = %d, ran = %v: %s", rec.Code, ran != nil, rec.Body.String())
		}
	})
}

// writeApprovalPolicy saves tools.approval as policy, or the default policy
// when policy is empty.
func writeApprovalPolicy(t *testing.T, path, policy string) {
	t.Helper()
	data := `{}`
	if policy != "" {
		data = `{"tools":{"approval":` + policy + `}}`
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func listedNeedsApproval(t *testing.T, mux *http.ServeMux, capability string) bool {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/modules", nil))
	var views []ModuleView
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("list: %v: %s", err, rec.Body.String())
	}
	for _, v := range views {
		for _, c := range v.Capabilities {
			if v.Module == "fake" && c.ID == capability {
				return c.NeedsApproval
			}
		}
	}
	t.Fatalf("the list does not show %s: %s", capability, rec.Body.String())
	return false
}

func invokeFake(mux *http.ServeMux, capability string, approved bool) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]any{
		"capability": capability, "input": map[string]any{"name": "hello"}, "approved": approved,
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/modules/fake/invoke", strings.NewReader(string(body))))
	return rec
}

// installAPIFakeModule builds the real fake module into home's modules
// directory.
func installAPIFakeModule(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(moduletools.ModulesDir(home), "fake")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "fake"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	command := exec.Command("go", "build", "-tags", "goolm,stdjson", "-o", filepath.Join(dir, name), "../../../cmd/fakemodule")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build fake module: %v: %s", err, output)
	}
}
