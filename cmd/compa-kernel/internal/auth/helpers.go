package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v3/pkg/auth"
	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/modelservice"
)

const supportedProvidersMsg = "supported providers: openai, anthropic"

// apiKeyProviders are the providers `auth login` connects with an API key.
var apiKeyProviders = []string{modelservice.APIKeyProviderOpenAI, modelservice.APIKeyProviderAnthropic}

// connectDeps is overridden by tests so they never reach the network.
var connectDeps = modelservice.ConnectDeps{}

// connectTimeout bounds catalog discovery while connecting a provider.
const connectTimeout = 60 * time.Second

// statusOK is the status of a command that did what it was asked, as the
// launcher's /api/credentials answers report it.
const statusOK = "ok"

// loginResult is what `auth login` connected: the fields of the launcher's
// POST /api/credentials answer, then the provider instance it made.
type loginResult struct {
	Status       string `json:"status"`
	Provider     string `json:"provider"`
	InstanceID   string `json:"instance_id"`
	ModelCount   int    `json:"model_count"`
	DefaultModel string `json:"default_model"`
}

// authLoginCmd stores an API key for a provider and connects it: the provider
// becomes an instance whose models are added to the chat shortlist. It
// prompts for the key on prompt.
func authLoginCmd(provider string, prompt io.Writer) (loginResult, error) {
	switch provider {
	case modelservice.APIKeyProviderOpenAI, modelservice.APIKeyProviderAnthropic:
	default:
		return loginResult{}, fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
	}
	key, err := readAPIKey(provider, os.Stdin, prompt)
	if err != nil {
		return loginResult{}, fmt.Errorf("login failed: %w", err)
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
// The prompt goes to out.
func readAPIKey(provider string, in *os.File, out io.Writer) (string, error) {
	if !stdinIsTerminal(in) {
		cred, err := auth.LoginPasteTokenWithPrompt(provider, in, out)
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

func connectProvider(provider, key string) (loginResult, error) {
	configPath := internal.GetConfigPath()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := modelservice.ConnectAPIKeyProvider(ctx, configPath, provider, key, connectDeps); err != nil {
		return loginResult{}, fmt.Errorf("could not connect %s (the key was not saved): %w", provider, err)
	}

	cfg, err := loadConfig(configPath)
	if err != nil {
		return loginResult{}, fmt.Errorf("connected %s, but could not read the config: %w", provider, err)
	}
	instance, err := modelservice.APIKeyProviderInstance(provider)
	if err != nil {
		return loginResult{}, err
	}
	models := 0
	if catalogs, catalogErr := modelservice.LoadInstanceCatalogs(cfg); catalogErr == nil {
		models = len(catalogs[instance.ID].Models)
	}
	return loginResult{
		Status:       statusOK,
		Provider:     provider,
		InstanceID:   instance.ID,
		ModelCount:   models,
		DefaultModel: cfg.Agents.Defaults.GetModelName(),
	}, nil
}

func printLogin(w io.Writer, login loginResult) {
	fmt.Fprintf(w, "Connected %s as provider instance %q (%d models available).\n",
		login.Provider, login.InstanceID, login.ModelCount)
	if login.DefaultModel != "" {
		fmt.Fprintf(w, "Default model: %s\n", login.DefaultModel)
	} else {
		fmt.Fprintf(w, "Choose a default model with: compa-kernel model %s/<model-id>\n", login.InstanceID)
	}
}

func loadConfig(path string) (*config.Config, error) {
	if connectDeps.LoadConfig != nil {
		return connectDeps.LoadConfig(path)
	}
	return config.LoadConfig(path)
}

// logoutResult is what `auth logout --json` prints: the status of the
// launcher's DELETE /api/credentials/{provider} answers, and the providers
// logged out.
type logoutResult struct {
	Status    string   `json:"status"`
	Providers []string `json:"providers"`
}

// authLogoutCmd removes a provider's key and disables its instance; with no
// provider it logs out of every API-key provider. It returns the providers
// it logged out of, also when logging out of another one failed.
func authLogoutCmd(provider string) ([]string, error) {
	targets := apiKeyProviders
	if provider != "" {
		switch provider {
		case modelservice.APIKeyProviderOpenAI, modelservice.APIKeyProviderAnthropic:
		default:
			return nil, fmt.Errorf("unsupported provider: %s (%s)", provider, supportedProvidersMsg)
		}
		targets = []string{provider}
	}
	configPath := internal.GetConfigPath()
	loggedOut := []string{}
	var errs []error
	for _, name := range targets {
		if err := modelservice.DisconnectAPIKeyProvider(configPath, name, connectDeps); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		loggedOut = append(loggedOut, name)
	}
	return loggedOut, errors.Join(errs...)
}

// The status of a stored credential.
const (
	credentialActive       = "active"
	credentialExpired      = "expired"
	credentialNeedsRefresh = "needs_refresh"
)

// credentialStatus is one stored credential without its secrets, under the
// auth store's field names.
type credentialStatus struct {
	Provider   string    `json:"provider"`
	AuthMethod string    `json:"auth_method"`
	Status     string    `json:"status"`
	AccountID  string    `json:"account_id,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
}

// statusResult is what `auth status --json` prints.
type statusResult struct {
	Providers []credentialStatus `json:"providers"`
	Total     int                `json:"total"`
}

// authStatusCmd lists the stored credentials, sorted by provider.
func authStatusCmd() ([]credentialStatus, error) {
	store, err := auth.LoadStore()
	if err != nil {
		return nil, fmt.Errorf("failed to load auth store: %w", err)
	}

	statuses := make([]credentialStatus, 0, len(store.Credentials))
	for provider, cred := range store.Credentials {
		if cred == nil {
			continue
		}
		status := credentialActive
		if cred.IsExpired() {
			status = credentialExpired
		} else if cred.NeedsRefresh() {
			status = credentialNeedsRefresh
		}
		statuses = append(statuses, credentialStatus{
			Provider:   provider,
			AuthMethod: cred.AuthMethod,
			Status:     status,
			AccountID:  cred.AccountID,
			ExpiresAt:  cred.ExpiresAt,
		})
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Provider < statuses[j].Provider })
	return statuses, nil
}

func printStatus(w io.Writer, statuses []credentialStatus) {
	if len(statuses) == 0 {
		fmt.Fprintln(w, "No authenticated providers.")
		fmt.Fprintln(w, "Run: compa-kernel auth login --provider <name>")
		return
	}

	fmt.Fprintln(w, "\nAuthenticated Providers:")
	fmt.Fprintln(w, "------------------------")
	for _, s := range statuses {
		fmt.Fprintf(w, "  %s:\n", s.Provider)
		fmt.Fprintf(w, "    Method: %s\n", s.AuthMethod)
		fmt.Fprintf(w, "    Status: %s\n", strings.ReplaceAll(s.Status, "_", " "))
		if s.AccountID != "" {
			fmt.Fprintf(w, "    Account: %s\n", s.AccountID)
		}
		if !s.ExpiresAt.IsZero() {
			fmt.Fprintf(w, "    Expires: %s\n", s.ExpiresAt.Format("2006-01-02 15:04"))
		}
	}
}
