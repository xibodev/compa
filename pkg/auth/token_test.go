package auth

import (
	"strings"
	"testing"
)

func TestLoginPasteToken(t *testing.T) {
	cred, err := LoginPasteToken("openai", strings.NewReader("  sk-test-key  \n"))
	if err != nil {
		t.Fatalf("LoginPasteToken() error = %v", err)
	}
	if cred.AccessToken != "sk-test-key" || cred.Provider != "openai" || cred.AuthMethod != "api_key" {
		t.Fatalf("credential = %#v", cred)
	}
}

func TestLoginPasteTokenRejectsEmptyInput(t *testing.T) {
	for _, input := range []string{"", "   \n"} {
		if _, err := LoginPasteToken("anthropic", strings.NewReader(input)); err == nil {
			t.Fatalf("LoginPasteToken(%q) error = nil, want error", input)
		}
	}
}
