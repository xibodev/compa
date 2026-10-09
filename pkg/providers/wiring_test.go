package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/providers/protocoltypes"
)

// The agent marks its cache breakpoints on the system message's parts; an
// Anthropic instance gets them, through translation, as cache_control on the
// system blocks of its Messages request.
func TestAnthropicInstanceKeepsSystemCacheControl(t *testing.T) {
	server, requests := recordingEndpoint(t, anthropicResponse)
	provider, err := CreateProviderFromInstance(coreInstance("anthropic", "anthropic", config.ProviderAdapterAnthropicCompatible, server.URL+"/v1"), "claude", "sk-ant")
	if err != nil {
		t.Fatal(err)
	}
	ephemeral := &protocoltypes.CacheControl{Type: "ephemeral"}
	system := Message{
		Role:    "system",
		Content: "static identity\n\n---\n\nnow",
		SystemParts: []protocoltypes.ContentBlock{
			{Type: "text", Text: "static identity", CacheControl: ephemeral},
			{Type: "text", Text: "now"},
		},
	}
	if _, err := provider.Chat(t.Context(), []Message{system, {Role: "user", Content: "hi"}}, nil, "claude", nil); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	raw, _ := json.Marshal(requests()[0].body["system"])
	var blocks []struct {
		Text         string         `json:"text"`
		CacheControl map[string]any `json:"cache_control"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil || len(blocks) == 0 {
		t.Fatalf("system = %s, want blocks", raw)
	}
	if blocks[0].Text != "static identity" || blocks[0].CacheControl["type"] != "ephemeral" {
		t.Fatalf("system = %s, want the static block with its cache_control", raw)
	}
	var text strings.Builder
	for _, block := range blocks {
		text.WriteString(block.Text)
	}
	if text.String() != system.Content {
		t.Fatalf("system text = %q, want %q", text.String(), system.Content)
	}
}

// Anthropic's 529 "overloaded" is retried and fails over, as a 503 is.
func TestAnthropicOverloadedIsRetryable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusOverloaded)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	defer server.Close()
	provider, err := CreateProviderFromInstance(coreInstance("anthropic", "anthropic", config.ProviderAdapterAnthropicCompatible, server.URL+"/v1"), "claude", "sk-ant")
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "claude", nil)
	if failure := DescribeFailure(err); failure.StatusCode != statusOverloaded || failure.Disposition != core.DispositionRetryable {
		t.Fatalf("failure = %+v (%v), want a retryable 529", failure, err)
	}
}

func TestCreatedProviderClosesItsIdleConnections(t *testing.T) {
	provider, err := CreateProviderFromInstance(coreInstance("o", "openai", config.ProviderAdapterOpenAICompatible, "https://api.example.test/v1"), "gpt", "key")
	if err != nil {
		t.Fatal(err)
	}
	stateful, ok := provider.(StatefulProvider)
	if !ok {
		t.Fatalf("%T is not a StatefulProvider, so its connection pool is never closed", provider)
	}
	stateful.Close()

	// An instance's headers wrap its transport; closing still reaches it.
	closed := 0
	headers := &headerTransport{base: closeCounter{&closed}}
	(&http.Client{Transport: headers}).CloseIdleConnections()
	if closed != 1 {
		t.Fatalf("idle connections closed %d times through the header transport, want once", closed)
	}
}

type closeCounter struct{ closed *int }

func (closeCounter) RoundTrip(*http.Request) (*http.Response, error) { return nil, io.EOF }

func (c closeCounter) CloseIdleConnections() { *c.closed++ }

func TestOnlySignedInExtensionCredentialsAreRefreshed(t *testing.T) {
	if InstanceCredentialRefresher(coreInstance("o", "openai", config.ProviderAdapterOpenAICompatible, "https://x.test")) != nil {
		t.Fatal("an API key instance got a refresher")
	}
	instance := coreInstance("ext", "extension", config.ProviderAdapterExtension, "http://127.0.0.1:1")
	instance.Settings = map[string]any{
		config.ExtensionProviderSetting:      "acme",
		config.ExtensionCredentialSetting:    "oauth",
		config.ExtensionCredentialKeySetting: "acme:owner",
	}
	original := ExtensionRejectedCredential
	t.Cleanup(func() { ExtensionRejectedCredential = original })
	var got []string
	ExtensionRejectedCredential = func(_ context.Context, endpoint, provider, key string, rejected *core.Credential) (*core.Credential, error) {
		got = append(got, endpoint, provider, key, rejected.Token)
		return &core.Credential{Token: "fresh"}, nil
	}
	refresh := InstanceCredentialRefresher(instance)
	if refresh == nil {
		t.Fatal("a signed-in extension instance got no refresher")
	}
	credential, err := refresh(t.Context(), &core.Credential{Token: "stale"})
	if err != nil || credential.Token != "fresh" || strings.Join(got, ",") != "http://127.0.0.1:1,acme,acme:owner,stale" {
		t.Fatalf("refresh = %v, %v; called with %v", credential, err, got)
	}
}

func TestCheckExtensionEndpoint(t *testing.T) {
	for endpoint, ok := range map[string]bool{
		"http://127.0.0.1:8787":       true,
		"http://localhost:8787":       true,
		"http://[::1]:8787":           true,
		"https://daemon.example.test": true,
		"http://daemon.example.test":  false,
		"http://192.168.1.20:8787":    false,
		"ftp://127.0.0.1":             false,
		"127.0.0.1:8787":              false,
	} {
		if err := CheckExtensionEndpoint(endpoint, "secret"); (err == nil) != ok {
			t.Errorf("CheckExtensionEndpoint(%q) = %v, want ok=%v", endpoint, err, ok)
		}
	}
	if err := CheckExtensionEndpoint("https://daemon.example.test", ""); err == nil {
		t.Error("a remote daemon without a secret was accepted")
	}
	if err := CheckExtensionEndpoint("http://127.0.0.1:8787", ""); err != nil {
		t.Errorf("a local daemon without a secret was refused: %v", err)
	}
}
