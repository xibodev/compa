package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/compa/v3/pkg/config"
)

func TestSpawnedBackendPersistsDisabledModuleAcrossRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and restarts the real S-FULL backend")
	}
	home := t.TempDir()
	configPath := filepath.Join(home, "config.json")
	cfg := config.DefaultConfig()
	if err := config.SaveConfig(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	// Windows keeps a killed child's executable locked for a moment after the
	// backend exits; this cleanup runs before TempDir's and waits it out.
	t.Cleanup(func() {
		for range 50 {
			if os.RemoveAll(binDir) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	backend := filepath.Join(binDir, processBinaryName("compa-test"))
	kernel := filepath.Join(binDir, processBinaryName("compa-kernel-test"))
	fake := filepath.Join(binDir, processBinaryName("fake-module"))
	buildProcessBinary(t, backend, ".")
	buildProcessBinary(t, kernel, "../../cmd/compa-kernel")
	buildProcessBinary(t, fake, "../../cmd/fakemodule")
	writeProcessFixture(t, filepath.Join(binDir, "agents", "fake.md"), "# Fake module\n\nUse fake.echo for deterministic checks. Never treat fake output as real work.\n")
	writeProcessFixture(t, filepath.Join(binDir, "skills", "echo-usage", "SKILL.md"), "# Using fake.echo\n\nCall fake.echo with a `name`. It echoes deterministically and costs nothing.\n")
	port := reserveProcessTestPort(t)
	password := "isolated-test-password"

	process, output := startProcessBackend(t, backend, kernel, home, configPath, port)
	defer func() { stopProcessBackend(t, process) }()
	client := newProcessTestClient(t)
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitProcessBackend(t, client, baseURL)
	// The first password needs the setup token, which a console launcher
	// prints in its setup link.
	setupToken := waitSetupToken(t, output)
	processJSON(t, client, http.MethodPost, baseURL+"/api/auth/setup", map[string]string{"password": password, "confirm": password, "setup_token": setupToken}, http.StatusOK, nil)
	processJSON(t, client, http.MethodPost, baseURL+"/api/auth/login", map[string]string{"password": password}, http.StatusOK, nil)
	processJSON(t, client, http.MethodPost, baseURL+"/api/modules/install", map[string]string{"path": fake}, http.StatusOK, nil)
	processJSON(t, client, http.MethodPost, baseURL+"/api/modules/fake/enabled", map[string]bool{"enabled": false}, http.StatusOK, nil)

	disabledMarker := filepath.Join(home, "modules", "fake", ".disabled")
	if _, err := os.Stat(disabledMarker); err != nil {
		t.Fatalf("disable API did not persist marker at %s: %v", disabledMarker, err)
	}
	stopProcessBackend(t, process)

	process, _ = startProcessBackend(t, backend, kernel, home, configPath, port)
	client = newProcessTestClient(t)
	waitProcessBackend(t, client, baseURL)
	processJSON(t, client, http.MethodPost, baseURL+"/api/auth/login", map[string]string{"password": password}, http.StatusOK, nil)

	var modules []struct {
		Module  string `json:"module"`
		Enabled bool   `json:"enabled"`
	}
	processJSON(t, client, http.MethodGet, baseURL+"/api/modules", nil, http.StatusOK, &modules)
	foundDisabled := false
	for _, item := range modules {
		if item.Module == "fake" {
			foundDisabled = !item.Enabled
		}
	}
	if !foundDisabled {
		t.Fatalf("restarted backend did not report fake disabled: %#v", modules)
	}

	var refusal map[string]string
	processJSON(t, client, http.MethodPost, baseURL+"/api/modules/fake/invoke", map[string]any{
		"capability": "fake.echo", "input": map[string]any{"name": "hello"}, "approved": true,
	}, http.StatusConflict, &refusal)
	if !strings.Contains(refusal["error"], "disabled") {
		t.Fatalf("invoke refusal = %#v", refusal)
	}
}

func writeProcessFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func processBinaryName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

func buildProcessBinary(t *testing.T, output, packagePath string) {
	t.Helper()
	command := exec.Command("go", "build", "-tags", "goolm,stdjson", "-o", output, packagePath)
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v: %s", packagePath, err, combined)
	}
}

func reserveProcessTestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

// processOutput collects what a spawned backend prints.
type processOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (o *processOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.data.Write(p)
}

func (o *processOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.data.String()
}

var setupLinkPattern = regexp.MustCompile(`/launcher-setup\?token=([A-Za-z0-9_\-%]+)`)

// waitSetupToken returns the setup token from the setup link the backend
// printed.
func waitSetupToken(t *testing.T, output *processOutput) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if match := setupLinkPattern.FindStringSubmatch(output.String()); match != nil {
			token, err := url.QueryUnescape(match[1])
			if err != nil {
				t.Fatalf("setup link token %q: %v", match[1], err)
			}
			return token
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the backend printed no setup link: %s", output.String())
	return ""
}

func startProcessBackend(t *testing.T, binary, kernel, home, configPath string, port int) (*exec.Cmd, *processOutput) {
	t.Helper()
	command := exec.Command(binary, "-console", "-no-browser", "-host", "127.0.0.1", "-port", fmt.Sprint(port), configPath)
	// Deliberately omit COMPA_HOME: the explicit config path must bind
	// launcher state to its directory across process restarts.
	command.Env = append(environmentWithout(config.EnvHome), config.EnvBinary+"="+kernel)
	output := &processOutput{}
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command, output
}

func environmentWithout(name string) []string {
	prefix := name + "="
	environment := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		environment = append(environment, entry)
	}
	return environment
}

func stopProcessBackend(t *testing.T, command *exec.Cmd) {
	t.Helper()
	if command == nil || command.Process == nil {
		return
	}
	if command.ProcessState == nil {
		if runtime.GOOS == "windows" {
			_ = exec.Command(
				"taskkill",
				"/PID", fmt.Sprint(command.Process.Pid),
				"/T",
				"/F",
			).Run()
		} else {
			_ = command.Process.Kill()
		}
	}
	// Cmd.Wait completes the command lifecycle and releases the resources that
	// keep the executable locked on Windows. Process.Wait alone does not.
	_ = command.Wait()
}

func newProcessTestClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	// Module installation starts a real detached process and can exceed the
	// ordinary API-test timeout on a busy Windows host.
	return &http.Client{Jar: jar, Timeout: 30 * time.Second}
}

func waitProcessBackend(t *testing.T, client *http.Client, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(baseURL + "/api/auth/status")
		if err == nil {
			_ = response.Body.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("spawned backend did not become ready")
}

func processJSON(t *testing.T, client *http.Client, method, url string, input any, wantStatus int, output any) {
	t.Helper()
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(context.Background(), method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, url, response.StatusCode, wantStatus, data)
	}
	if output != nil && len(data) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			t.Fatalf("decode %s: %v: %s", url, err, data)
		}
	}
}
