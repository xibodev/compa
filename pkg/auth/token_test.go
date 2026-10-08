package auth

import (
	"io"
	"os"
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

// The prompt goes to the writer given, never to stdout.
func TestLoginPasteTokenWithPromptWritesPromptToWriter(t *testing.T) {
	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var prompt strings.Builder
	cred, err := LoginPasteTokenWithPrompt("openai", strings.NewReader("sk-test-key\n"), &prompt)
	os.Stdout = stdout
	if closeErr := w.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	printed, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if err != nil {
		t.Fatalf("LoginPasteTokenWithPrompt() error = %v", err)
	}
	if cred.AccessToken != "sk-test-key" {
		t.Fatalf("credential = %#v", cred)
	}
	if got := prompt.String(); got != "Paste your API key from platform.openai.com:\n> " {
		t.Fatalf("prompt = %q", got)
	}
	if len(printed) != 0 {
		t.Fatalf("stdout = %q, want nothing", printed)
	}
}

func TestLoginPasteTokenRejectsEmptyInput(t *testing.T) {
	for _, input := range []string{"", "   \n"} {
		if _, err := LoginPasteToken("anthropic", strings.NewReader(input)); err == nil {
			t.Fatalf("LoginPasteToken(%q) error = nil, want error", input)
		}
	}
}
