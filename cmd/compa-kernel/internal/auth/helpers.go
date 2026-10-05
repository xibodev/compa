package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/xibodev/compa/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/pkg/auth"
	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/modelservice"
)

const supportedProvidersMsg = "supported providers: openai, anthropic"

// apiKeyProviders are the providers `auth login` connects with an API key.
var apiKeyProviders = []string{modelservice.APIKeyProviderOpenAI, modelservice.APIKeyProviderAnthropic}

// connectDeps is overridden by tests so they never reach the network.
var connectDeps = modelservice.ConnectDeps{}

// connectTimeout bounds catalog discovery while connecting a provider.
const connectTimeout = 60 * time.Second

// authLoginCmd stores an API key for a provider and connects it: the provider
// becomes an instance whose models are added to the chat shortlist.
func authLoginCmd(provider string) error {
	switch provider {
	case modelservice.APIKeyProviderOpenAI, modelservice.APIKeyProviderAnthropic:
	default:
		return fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
	}
	key, err := readAPIKey(provider, os.Stdin, os.Stdout)
	if err != nil {
		return fmt.Errorf("login failed: %w", err)
	}
	return connectProvider(provider, key)
}

// stdinIsTerminal and readHidden are variables so tests can stand in for a
// terminal.
var (
	stdinIsTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }
	readHidden      = func(f *os.File) ([]byte, error) { return term.ReadPassword(int(f.Fd())) }
)

// readAPIKey reads the key without echoing it when stdin is a terminal, so it
// doesn't stay on screen or in the scrollback; piped input is read as a line.
func readAPIKey(provider string, in *os.File, out io.Writer) (string, error) {
	if !stdinIsTerminal(in) {
		cred, err := auth.LoginPasteToken(provider, in)
		if err != nil {
			return "", err
		}
		return cred.AccessToken, nil
	}
	fmt.Fprintf(out, "Paste your %s API key (input hidden): ", provider)
	raw, err := readHidden(in)
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("reading key: %w", err)
	}
	key := strings.TrimSpace(string(raw))
	if key == "" {
		return "", fmt.Errorf("token cannot be empty")
	}
	return key, nil
}

func connectProvider(provider, key string) error {
	configPath := internal.GetConfigPath()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := modelservice.ConnectAPIKeyProvider(ctx, configPath, provider, key, connectDeps); err != nil {
		return fmt.Errorf("could not connect %s (the key was not saved): %w", provider, err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return fmt.Errorf("connected %s, but could not read the config: %w", provider, err)
	}
	instance, err := modelservice.APIKeyProviderInstance(provider)
	if err != nil {
		return err
	}
	models := 0
	if catalogs, catalogErr := modelservice.LoadInstanceCatalogs(cfg); catalogErr == nil {
		models = len(catalogs[instance.ID].Models)
	}
	fmt.Printf("Connected %s as provider instance %q (%d models available).\n", provider, instance.ID, models)
	if selection := cfg.Agents.Defaults.GetModelName(); selection != "" {
		fmt.Printf("Default model: %s\n", selection)
	} else {
		fmt.Printf("Choose a default model with: compa-kernel model %s/<model-id>\n", instance.ID)
	}
	return nil
}

func loadConfig(path string) (*config.Config, error) {
	if connectDeps.LoadConfig != nil {
		return connectDeps.LoadConfig(path)
	}
	return config.LoadConfig(path)
}

// authLogoutCmd removes a provider's key and disables its instance; with no
// provider it logs out of every API-key provider.
func authLogoutCmd(provider string) error {
	targets := apiKeyProviders
	if provider != "" {
		switch provider {
		case modelservice.APIKeyProviderOpenAI, modelservice.APIKeyProviderAnthropic:
		default:
			return fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
		}
		targets = []string{provider}
	}
	configPath := internal.GetConfigPath()
	var errs []error
	for _, name := range targets {
		if err := modelservice.DisconnectAPIKeyProvider(configPath, name, connectDeps); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		fmt.Printf("Logged out from %s\n", name)
	}
	return errors.Join(errs...)
}

func authStatusCmd() error {
	store, err := auth.LoadStore()
	if err != nil {
		return fmt.Errorf("failed to load auth store: %w", err)
	}

	if len(store.Credentials) == 0 {
		fmt.Println("No authenticated providers.")
		fmt.Println("Run: compa-kernel auth login --provider <name>")
		return nil
	}

	fmt.Println("\nAuthenticated Providers:")
	fmt.Println("------------------------")
	for provider, cred := range store.Credentials {
		status := "active"
		if cred.IsExpired() {
			status = "expired"
		} else if cred.NeedsRefresh() {
			status = "needs refresh"
		}

		fmt.Printf("  %s:\n", provider)
		fmt.Printf("    Method: %s\n", cred.AuthMethod)
		fmt.Printf("    Status: %s\n", status)
		if cred.AccountID != "" {
			fmt.Printf("    Account: %s\n", cred.AccountID)
		}
		if !cred.ExpiresAt.IsZero() {
			fmt.Printf("    Expires: %s\n", cred.ExpiresAt.Format("2006-01-02 15:04"))
		}
	}

	return nil
}
