package auth

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// LoginPasteToken prompts on stdout for provider's API key and reads it, one
// line, from r.
func LoginPasteToken(provider string, r io.Reader) (*AuthCredential, error) {
	return LoginPasteTokenWithPrompt(provider, r, os.Stdout)
}

// LoginPasteTokenWithPrompt is LoginPasteToken with the prompt written to
// prompt, so a command whose stdout another program reads can prompt on
// stderr.
func LoginPasteTokenWithPrompt(provider string, r io.Reader, prompt io.Writer) (*AuthCredential, error) {
	fmt.Fprintf(prompt, "Paste your API key from %s:\n", providerDisplayName(provider))
	fmt.Fprint(prompt, "> ")

	scanner := bufio.NewScanner(r)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("reading token: %w", err)
		}
		return nil, fmt.Errorf("no input received")
	}

	token := strings.TrimSpace(scanner.Text())
	if token == "" {
		return nil, fmt.Errorf("token cannot be empty")
	}

	return &AuthCredential{
		AccessToken: token,
		Provider:    provider,
		AuthMethod:  "api_key",
	}, nil
}

func providerDisplayName(provider string) string {
	switch provider {
	case "anthropic":
		return "console.anthropic.com"
	case "openai":
		return "platform.openai.com"
	default:
		return provider
	}
}
