package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// SaveConfig takes the lock beside config.json, so a save waits while another
// process holds it. The lock is taken here on its own handle, as another
// process would.
func TestSaveConfigWaitsForTheFileLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	f, err := os.OpenFile(path+".flock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- SaveConfig(path, DefaultConfig()) }()
	select {
	case err := <-done:
		t.Fatalf("SaveConfig finished while another holder had the lock: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := unlockFile(f); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SaveConfig() error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SaveConfig did not finish after the lock was released")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config.json was not written: %v", err)
	}
}

// .security.yml and config.json are replaced together: when config.json
// cannot be replaced, .security.yml gets its previous content back.
func TestSaveConfigRestoresSecurityWhenConfigReplaceFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	secPath := filepath.Join(dir, SecurityConfigFile)
	const oldConfig = `{"gateway":{"port":18790}}`
	const oldSecurity = "# previous secrets\n"
	if err := os.WriteFile(path, []byte(oldConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secPath, []byte(oldSecurity), 0o600); err != nil {
		t.Fatal(err)
	}

	renameFile = func(src, dst string) error {
		if dst == path {
			return errors.New("injected failure")
		}
		return os.Rename(src, dst)
	}
	defer func() { renameFile = os.Rename }()

	cfg := DefaultConfig()
	if err := SaveConfig(path, cfg); err == nil || !strings.Contains(err.Error(), "injected failure") {
		t.Fatalf("SaveConfig() error = %v, want the injected failure", err)
	}
	if got, _ := os.ReadFile(secPath); string(got) != oldSecurity {
		t.Fatalf(".security.yml = %q, want the previous content restored", got)
	}
	if got, _ := os.ReadFile(path); string(got) != oldConfig {
		t.Fatalf("config.json = %q, want it unchanged", got)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file %s was left behind", e.Name())
		}
	}
}

// Loading is strict: a setting this version does not know fails the load,
// and the error names it.
func TestLoadConfigRefusesUnknownFields(t *testing.T) {
	content := `{"version": 1, "tools": {"future_tool": {"enabled": true}}}`
	_, err := LoadConfig(writeConfigFile(t, content))
	if err == nil {
		t.Fatal("LoadConfig accepted unknown fields")
	}
	for _, field := range []string{"version", "tools.future_tool"} {
		if !strings.Contains(err.Error(), field) {
			t.Errorf("error does not name %q: %v", field, err)
		}
	}
}

// Parse errors print the offending line in logs and the UI, so values of
// secret-looking keys are masked there.
func TestParseErrorPreviewMasksSecretValues(t *testing.T) {
	for name, content := range map[string]string{
		"syntax error on the secret line": "{\n  \"api_key\": \"sk-live-123456\" oops\n}",
		"inside a secret list":            "{\n  \"api_keys\": [\n    \"sk-live-123456\" \"x\"\n  ]\n}",
		"minified":                        `{"gateway":{"token":"sk-live-123456","port":}}`,
	} {
		_, err := LoadConfig(writeConfigFile(t, content))
		if err == nil {
			t.Fatalf("%s: LoadConfig accepted malformed content", name)
		}
		if !strings.Contains(err.Error(), "^") {
			t.Fatalf("%s: the error has no preview to check: %v", name, err)
		}
		if strings.Contains(err.Error(), "sk-live-123456") {
			t.Errorf("%s: the error shows the secret:\n%v", name, err)
		}
	}

	// A type error names the JSON value it got; a secret's is reduced to its kind.
	data := []byte(`{"token": 987654321}`)
	err := wrapJSONError(data, &json.UnmarshalTypeError{
		Value: "number 987654321", Type: reflect.TypeOf(""), Offset: 19, Field: "channel_list.x.token",
	}, "config.json")
	if strings.Contains(err.Error(), "987654321") {
		t.Errorf("the type error shows the secret:\n%v", err)
	}

	// Values of other keys stay visible, since they are what needs fixing.
	_, err = LoadConfig(writeConfigFile(t, "{\n  \"gateway\": {\"host\": \"bad host\" oops}\n}"))
	if err == nil || !strings.Contains(err.Error(), "bad host") {
		t.Fatalf("error = %v, want the non-secret value in the preview", err)
	}
}

func TestMaskSecretJSONLineKeepsAlignment(t *testing.T) {
	line := `  "token": "abc\"def", "name": "x", "password": 1234, "api_keys": ["a", "b"]`
	got := maskSecretJSONLine(line, false)
	if len(got) != len(line) {
		t.Fatalf("masking changed the length: %q", got)
	}
	for _, leaked := range []string{"abc", "def", "1234", `"a"`, `"b"`} {
		if strings.Contains(got, leaked) {
			t.Errorf("%q still shows %s", got, leaked)
		}
	}
	if !strings.Contains(got, `"name": "x"`) {
		t.Errorf("a non-secret value was masked: %q", got)
	}
}

// A key names a secret by its last word: "max_tokens" is a count, not a token.
func TestSecretLookingKeysGoByTheirLastWord(t *testing.T) {
	for key, want := range map[string]bool{
		"api_key": true, "api_keys": true, "token": true, "bot_token": true, "client_secret": true,
		"password": true, "crypto_passphrase": true, "channel_list.x.token": true,
		"GITHUB_PERSONAL_ACCESS_TOKEN": true, "X-Api-Key": true, "apiKey": true,
		"max_tokens": false, "max_completion_tokens": false, "tokenizer": false, "keywords": false,
		"name": false, "token_file": false,
	} {
		if got := isSecretLookingKey(key); got != want {
			t.Errorf("isSecretLookingKey(%q) = %v, want %v", key, got, want)
		}
	}
	line := `  "max_tokens": 4096, "api_key": "sk-live-123456"`
	got := maskSecretJSONLine(line, false)
	if !strings.Contains(got, `"max_tokens": 4096`) || strings.Contains(got, "sk-live-123456") {
		t.Errorf("maskSecretJSONLine() = %q, want only the api_key masked", got)
	}
}
