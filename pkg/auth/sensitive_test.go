package auth

import (
	"slices"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
)

func TestStoredSecretsListsEveryStoredToken(t *testing.T) {
	setTestAuthHome(t)
	if got := storedSecrets(); len(got) != 0 {
		t.Fatalf("storedSecrets() without a store = %q, want none", got)
	}

	if err := SetCredential("openai", &AuthCredential{AccessToken: "sk-openai-key", AuthMethod: "api_key"}); err != nil {
		t.Fatal(err)
	}
	if err := SetCredential("extension-signin:acme", &AuthCredential{
		AccessToken:  "acme-access",
		RefreshToken: "acme-refresh",
		IDToken:      "acme-id-token",
		AuthMethod:   "oauth",
	}); err != nil {
		t.Fatal(err)
	}
	if err := SetCredential("duplicate", &AuthCredential{AccessToken: "sk-openai-key", RefreshToken: "  "}); err != nil {
		t.Fatal(err)
	}

	want := []string{"acme-access", "acme-id-token", "acme-refresh", "sk-openai-key"}
	if got := storedSecrets(); !slices.Equal(got, want) {
		t.Fatalf("storedSecrets() = %q, want %q", got, want)
	}
}

func TestStoredSecretsRereadsOnlyWhenTheStoreChanges(t *testing.T) {
	setTestAuthHome(t)
	reads := 0
	original := loadSecretsStore
	loadSecretsStore = func(path string) (*AuthStore, error) {
		reads++
		return original(path)
	}
	t.Cleanup(func() { loadSecretsStore = original })

	if err := SetCredential("openai", &AuthCredential{AccessToken: "sk-first"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if got := storedSecrets(); !slices.Equal(got, []string{"sk-first"}) {
			t.Fatalf("storedSecrets() = %q", got)
		}
	}
	if reads != 1 {
		t.Fatalf("auth.json read %d times for an unchanged store, want 1", reads)
	}

	if err := SetCredential("openai", &AuthCredential{AccessToken: "sk-second-longer"}); err != nil {
		t.Fatal(err)
	}
	if got := storedSecrets(); !slices.Equal(got, []string{"sk-second-longer"}) {
		t.Fatalf("storedSecrets() after a change = %q, want the new key", got)
	}
	if reads != 2 {
		t.Fatalf("auth.json read %d times, want a reread after the change", reads)
	}
}

// TestFilterSensitiveDataFiltersStoredCredentials pins the wiring: a key the
// auth store holds is filtered by any config, including a key stored after
// the config was first filtered.
func TestFilterSensitiveDataFiltersStoredCredentials(t *testing.T) {
	setTestAuthHome(t)
	cfg := &config.Config{Tools: config.ToolsConfig{FilterSensitiveData: true, FilterMinLength: 8}}

	if err := SetCredential("openai", &AuthCredential{AccessToken: "sk-stored-openai-key"}); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.FilterSensitiveData("tool saw sk-stored-openai-key"), "tool saw [FILTERED]"; got != want {
		t.Fatalf("FilterSensitiveData() = %q, want %q", got, want)
	}

	if err := SetCredential("anthropic", &AuthCredential{AccessToken: "sk-ant-added-later"}); err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.FilterSensitiveData("then sk-ant-added-later"), "then [FILTERED]"; got != want {
		t.Fatalf("FilterSensitiveData() after a new key = %q, want %q", got, want)
	}
}
