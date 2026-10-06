package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xibodev/llm-provider-auth/tokenstore"
	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/extension"
	"github.com/xibodev/llmgw-core/oauthflow"

	"github.com/xibodev/compa/v2/pkg/auth"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/providers"
	"github.com/xibodev/compa/v2/pkg/providers/protocoltypes"
)

const testDaemonSecret = "daemon-secret"

// fakeExtensionDaemon serves the extension protocol for three chat providers:
// one keyless, one taking a pasted token, and one signed in to; a keyless
// speech-only provider; an embeddings-only provider whose catalog fails; and
// one that does not declare its credential, which Compa cannot serve.
type fakeExtensionDaemon struct {
	mu         sync.Mutex
	server     *httptest.Server
	approved   bool
	refreshes  int
	lastTokens map[string]string // route -> credential token seen
	// pastedSignIn makes the pasted-token provider ask for a sign-in.
	pastedSignIn bool
}

func newFakeExtensionDaemon(t *testing.T) *fakeExtensionDaemon {
	t.Helper()
	d := &fakeExtensionDaemon{lastTokens: map[string]string{}}
	d.server = httptest.NewServer(http.HandlerFunc(d.serve))
	t.Cleanup(d.server.Close)
	return d
}

func (d *fakeExtensionDaemon) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testDaemonSecret {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	route := strings.TrimPrefix(r.URL.Path, extension.PathPrefix)
	d.mu.Lock()
	defer d.mu.Unlock()
	if credential := extension.CredentialFromHeaders(r.Header); credential != nil {
		d.lastTokens[route] = credential.Token
	} else {
		d.lastTokens[route] = ""
	}
	w.Header().Set("Content-Type", "application/json")
	switch route {
	case "info":
		pasted := extension.ProviderInfo{ID: "pasted", Name: "Pasted", Surfaces: []core.ModelSurface{core.ModelSurfaceChatCompletions}, Credential: extension.CredentialToken}
		if d.pastedSignIn {
			pasted.Credential, pasted.HasOAuth, pasted.OAuthMethods = extension.CredentialOAuth, true, []oauthflow.Method{oauthflow.MethodDevice}
		}
		_ = json.NewEncoder(w).Encode(extension.InfoResponse{Version: "test", Providers: []extension.ProviderInfo{
			{ID: "free", Name: "Free", Surfaces: []core.ModelSurface{core.ModelSurfaceChatCompletions}, Credential: extension.CredentialNone},
			pasted,
			{ID: "account", Name: "Account", Surfaces: []core.ModelSurface{core.ModelSurfaceChatCompletions}, Credential: extension.CredentialOAuth,
				HasOAuth: true, HasRefresh: true, OAuthMethods: []oauthflow.Method{oauthflow.MethodDevice, oauthflow.MethodManual}},
			{ID: "vectors", Name: "Vectors", Surfaces: []core.ModelSurface{core.ModelSurfaceEmbeddings}, Credential: extension.CredentialNone},
			{ID: "undeclared", Name: "Undeclared", Surfaces: []core.ModelSurface{core.ModelSurfaceChatCompletions}},
			{ID: "speaker", Name: "Speaker", Surfaces: []core.ModelSurface{core.ModelSurfaceAudioSpeech}, Credential: extension.CredentialNone},
		}})
	case "speaker/models":
		_ = json.NewEncoder(w).Encode(extension.ModelsResponse{Models: []core.ModelInfo{{ID: "speaker-voice"}}})
	case "speaker/invoke":
		if r.Header.Get(extension.HeaderSurface) != string(core.ModelSurfaceAudioSpeech) {
			http.Error(w, `{"error":"wrong surface"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "audio/ogg")
		_, _ = io.WriteString(w, "OggS-speaker")
	case "free/models", "pasted/models", "account/models":
		provider := strings.TrimSuffix(route, "/models")
		_ = json.NewEncoder(w).Encode(extension.ModelsResponse{Models: []core.ModelInfo{{ID: provider + "-model"}}})
	case "account/oauth/start":
		var req extension.OAuthStartRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		authorization := oauthflow.Authorization{Secrets: oauthflow.Secrets{State: "expected-state", DeviceCode: "device-code"}}
		if req.Method == oauthflow.MethodDevice {
			authorization.UserCode, authorization.VerificationURI, authorization.Interval = "ABCD-EFGH", "https://example.test/device", time.Second
		} else {
			authorization.AuthorizationURL = "https://example.test/authorize?state=expected-state"
		}
		_ = json.NewEncoder(w).Encode(extension.OAuthStartResponse{Authorization: authorization})
	case "account/oauth/poll":
		if !d.approved {
			_ = json.NewEncoder(w).Encode(extension.OAuthPollResponse{Result: oauthflow.PollResult{Status: oauthflow.PollPending}})
			return
		}
		_ = json.NewEncoder(w).Encode(extension.OAuthPollResponse{Result: oauthflow.PollResult{Status: oauthflow.PollApproved, Record: tokenstore.Record{
			AccessToken: "device-access", RefreshToken: "device-refresh", Expiry: time.Now().Add(time.Hour), AccountID: "acct",
		}}})
	case "account/oauth/exchange":
		var req extension.OAuthExchangeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Code != "good-code" || req.Flow.Secrets.State != "expected-state" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(extension.OAuthExchangeResponse{Error: "bad code"})
			return
		}
		_ = json.NewEncoder(w).Encode(extension.OAuthExchangeResponse{Record: tokenstore.Record{
			AccessToken: "manual-access", RefreshToken: "manual-refresh", Expiry: time.Now().Add(time.Hour), AccountID: "acct",
		}})
	case "account/refresh":
		d.refreshes++
		var req extension.RefreshRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		next := req.Record
		next.AccessToken = fmt.Sprintf("refreshed-%d", d.refreshes)
		next.Expiry = time.Now().Add(time.Hour)
		_ = json.NewEncoder(w).Encode(extension.RefreshResponse{Record: next})
	case "account/invoke", "pasted/invoke", "free/invoke":
		_, _ = io.WriteString(w, `{"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	case "account/stream", "pasted/stream", "free/stream":
		// Compa streams every chat call to a daemon.
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	default:
		http.Error(w, `{"error":"no route"}`, http.StatusNotFound)
	}
}

func (d *fakeExtensionDaemon) token(route string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastTokens[route]
}

func extensionRequest(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, reader))
	return rec
}

func loadExtensionInstance(t *testing.T, configPath, id string) *config.ProviderInstanceConfig {
	t.Helper()
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	index := providerInstanceIndex(cfg, id)
	if index < 0 {
		return nil
	}
	return cfg.ProviderInstances[index]
}

func TestExtensionDaemonConnectAndCredentials(t *testing.T) {
	configPath, cleanup := setupCredentialTestEnv(t)
	defer cleanup()
	resetCredentialHooks(t)
	daemon := newFakeExtensionDaemon(t)

	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)

	// A wrong secret is refused before anything is saved.
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": daemon.server.URL, "secret": "wrong"}); rec.Code != http.StatusBadGateway {
		t.Fatalf("wrong secret status = %d, body=%s", rec.Code, rec.Body.String())
	}

	rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": daemon.server.URL, "secret": testDaemonSecret})
	if rec.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var status extensionStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != "connected" || !status.HasSecret || len(status.Providers) != 6 {
		t.Fatalf("status = %+v", status)
	}
	// The stored secret is not reused for a different URL.
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": "http://127.0.0.1:1", "keep_secret": true}); rec.Code != http.StatusBadRequest {
		t.Fatalf("keep_secret with a new URL status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": daemon.server.URL, "keep_secret": true}); rec.Code != http.StatusOK {
		t.Fatalf("keep_secret with the same URL status = %d, body=%s", rec.Code, rec.Body.String())
	}
	for _, view := range status.Providers {
		if (view.ID == "vectors" || view.ID == "speaker") && !view.Supported {
			t.Fatalf("a provider serving only non-chat core surfaces was refused: %+v", view)
		}
		if view.ID == "undeclared" && (view.Supported || view.Credential != "" || !strings.Contains(view.Reason, "declare")) {
			t.Fatalf("a provider that does not declare its credential was offered: %+v", view)
		}
	}

	if free := loadExtensionInstance(t, configPath, "ext-free"); free == nil || free.State != config.ProviderInstanceStateEnabled {
		t.Fatalf("keyless provider = %+v, want enabled", free)
	}
	if pasted := loadExtensionInstance(t, configPath, "ext-pasted"); pasted == nil || pasted.State != config.ProviderInstanceStateDisabled {
		t.Fatalf("token provider = %+v, want disabled until a token is saved", pasted)
	}
	if loadExtensionInstance(t, configPath, "ext-undeclared") != nil {
		t.Fatal("unsupported provider got an instance")
	}
	// A keyless provider whose catalog fails stays disabled.
	if vectors := loadExtensionInstance(t, configPath, "ext-vectors"); vectors == nil || vectors.State != config.ProviderInstanceStateDisabled ||
		vectors.Protocol != string(core.ModelSurfaceEmbeddings) {
		t.Fatalf("embeddings-only provider = %+v, want a disabled embeddings instance", vectors)
	}

	// Pasted token.
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/providers/pasted/token", map[string]any{"token": "pasted-token"}); rec.Code != http.StatusOK {
		t.Fatalf("token status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if got := daemon.token("pasted/models"); got != "pasted-token" {
		t.Fatalf("catalog credential = %q", got)
	}
	pasted := loadExtensionInstance(t, configPath, "ext-pasted")
	if pasted.State != config.ProviderInstanceStateEnabled || pasted.AuthConnectionRef != "credential:extension:pasted" {
		t.Fatalf("token provider = %+v", pasted)
	}

	// Device sign-in.
	rec = extensionRequest(t, mux, http.MethodPost, "/api/extension/providers/account/signin", map[string]any{"method": "device"})
	if rec.Code != http.StatusOK {
		t.Fatalf("device start status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var flow extensionFlowResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &flow)
	if flow.UserCode != "ABCD-EFGH" || flow.VerificationURI == "" {
		t.Fatalf("device flow = %+v", flow)
	}
	rec = extensionRequest(t, mux, http.MethodPost, "/api/extension/signin/"+flow.FlowID+"/poll", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &flow)
	if flow.Status != "pending" {
		t.Fatalf("first poll = %+v", flow)
	}
	daemon.mu.Lock()
	daemon.approved = true
	daemon.mu.Unlock()
	rec = extensionRequest(t, mux, http.MethodPost, "/api/extension/signin/"+flow.FlowID+"/poll", nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &flow)
	if rec.Code != http.StatusOK || flow.Status != "approved" {
		t.Fatalf("approved poll status = %d %+v body=%s", rec.Code, flow, rec.Body.String())
	}
	if got := daemon.token("account/models"); got != "device-access" {
		t.Fatalf("signed-in catalog credential = %q", got)
	}
	account := loadExtensionInstance(t, configPath, "ext-account")
	if account.State != config.ProviderInstanceStateEnabled || account.AuthConnectionRef != "" {
		t.Fatalf("signed-in provider = %+v", account)
	}

	// An expired sign-in refreshes through the daemon when a chat needs it.
	store := auth.DefaultTokenStore()
	record, err := store.Load(context.Background(), "extension-signin:account")
	if err != nil {
		t.Fatal(err)
	}
	record.Expiry = time.Now().Add(-time.Minute)
	if _, err := store.Save(context.Background(), "extension-signin:account", record); err != nil {
		t.Fatal(err)
	}
	provider, err := providers.CreateProviderFromInstance(account, "account-model", "")
	if err != nil {
		t.Fatalf("CreateProviderFromInstance() error = %v", err)
	}
	if _, err := provider.Chat(context.Background(), []protocoltypes.Message{{Role: "user", Content: "hi"}}, nil, "account-model", nil); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if got := daemon.token("account/stream"); got != "refreshed-1" {
		t.Fatalf("chat credential = %q, want the refreshed token", got)
	}

	// Manual sign-in: a redirect from another sign-in is refused, the right
	// code is accepted.
	rec = extensionRequest(t, mux, http.MethodPost, "/api/extension/providers/account/signin", map[string]any{"method": "manual"})
	_ = json.Unmarshal(rec.Body.Bytes(), &flow)
	if flow.AuthorizationURL == "" {
		t.Fatalf("manual flow = %+v", flow)
	}
	wrong := "https://example.test/callback?" + url.Values{"code": {"good-code"}, "state": {"other"}}.Encode()
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/signin/"+flow.FlowID+"/complete", map[string]any{"code": wrong}); rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatched state status = %d", rec.Code)
	}
	right := "https://example.test/callback?" + url.Values{"code": {"good-code"}, "state": {"expected-state"}}.Encode()
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/signin/"+flow.FlowID+"/complete", map[string]any{"code": right}); rec.Code != http.StatusOK {
		t.Fatalf("manual complete status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if record, err := store.Load(context.Background(), "extension-signin:account"); err != nil || record.AccessToken != "manual-access" {
		t.Fatalf("stored sign-in = %v, %v", record, err)
	}

	// Sign out.
	if rec := extensionRequest(t, mux, http.MethodDelete, "/api/extension/providers/account/credential", nil); rec.Code != http.StatusOK {
		t.Fatalf("sign out status = %d", rec.Code)
	}
	if account := loadExtensionInstance(t, configPath, "ext-account"); account.State != config.ProviderInstanceStateDisabled {
		t.Fatalf("signed-out provider = %+v", account)
	}
	if _, err := store.Load(context.Background(), "extension-signin:account"); err != tokenstore.ErrNotFound {
		t.Fatalf("sign-in still stored: %v", err)
	}

	// Disconnect.
	if rec := extensionRequest(t, mux, http.MethodDelete, "/api/extension", nil); rec.Code != http.StatusOK {
		t.Fatalf("disconnect status = %d", rec.Code)
	}
	if free := loadExtensionInstance(t, configPath, "ext-free"); free.State != config.ProviderInstanceStateDisabled {
		t.Fatalf("keyless provider after disconnect = %+v", free)
	}
	if secret, _ := auth.GetCredential(auth.ExtensionDaemonKey); secret != nil {
		t.Fatal("daemon secret kept after disconnect")
	}
}

func extensionTestMux(t *testing.T) (*http.ServeMux, string, *fakeExtensionDaemon) {
	t.Helper()
	configPath, cleanup := setupCredentialTestEnv(t)
	t.Cleanup(cleanup)
	resetCredentialHooks(t)
	daemon := newFakeExtensionDaemon(t)
	h := NewHandler(configPath)
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": daemon.server.URL, "secret": testDaemonSecret}); rec.Code != http.StatusOK {
		t.Fatalf("connect status = %d, body=%s", rec.Code, rec.Body.String())
	}
	return mux, configPath, daemon
}

func reconnectExtension(t *testing.T, mux http.Handler, daemon *fakeExtensionDaemon) {
	t.Helper()
	if rec := extensionRequest(t, mux, http.MethodPut, "/api/extension", map[string]any{"url": daemon.server.URL, "keep_secret": true}); rec.Code != http.StatusOK {
		t.Fatalf("reconnect status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestExtensionReconnectKeepsInstanceRuntimeAndHeaders(t *testing.T) {
	mux, configPath, daemon := extensionTestMux(t)
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/providers/pasted/token", map[string]any{"token": "pasted-token"}); rec.Code != http.StatusOK {
		t.Fatalf("token status = %d, body=%s", rec.Code, rec.Body.String())
	}
	streaming := true
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		for _, id := range []string{"ext-free", "ext-pasted"} {
			instance := cfg.ProviderInstances[providerInstanceIndex(cfg, id)]
			instance.Runtime = &config.ProviderInstanceRuntime{RequestTimeout: 45, Streaming: &streaming, ExtraBody: map[string]any{"store": false}}
			instance.Headers = map[string]string{"X-Team": "studio"}
		}
	})

	reconnectExtension(t, mux, daemon)

	for _, id := range []string{"ext-free", "ext-pasted"} {
		instance := loadExtensionInstance(t, configPath, id)
		if instance.State != config.ProviderInstanceStateEnabled {
			t.Fatalf("%s after reconnect = %+v, want enabled", id, instance)
		}
		runtime := instance.Runtime
		if runtime == nil || runtime.RequestTimeout != 45 || runtime.Streaming == nil || !*runtime.Streaming || runtime.ExtraBody["store"] != false {
			t.Fatalf("%s runtime after reconnect = %#v", id, runtime)
		}
		if instance.Headers["X-Team"] != "studio" {
			t.Fatalf("%s headers after reconnect = %#v", id, instance.Headers)
		}
	}
	if pasted := loadExtensionInstance(t, configPath, "ext-pasted"); pasted.AuthConnectionRef != "credential:extension:pasted" {
		t.Fatalf("ext-pasted credential after reconnect = %q", pasted.AuthConnectionRef)
	}
}

func TestExtensionReconnectDropsTargetsOfAProviderWhoseCredentialChanged(t *testing.T) {
	mux, configPath, daemon := extensionTestMux(t)
	if rec := extensionRequest(t, mux, http.MethodPost, "/api/extension/providers/pasted/token", map[string]any{"token": "pasted-token"}); rec.Code != http.StatusOK {
		t.Fatalf("token status = %d, body=%s", rec.Code, rec.Body.String())
	}
	editSavedConfig(t, configPath, func(cfg *config.Config) {
		cfg.ActiveModels = []string{"ext-pasted/pasted-model", "ext-free/free-model"}
		cfg.ModelRoutes = []*config.ModelRouteConfig{{Name: "pasted-route", Targets: []string{"ext-pasted/pasted-model"}}}
		cfg.Agents.Defaults.ModelName = "pasted-route"
		cfg.Agents.Defaults.ImageModel = "ext-free/free-model"
	})

	// The provider now asks for a sign-in, so the pasted token no longer
	// works and the instance is disabled; a route left targeting it would
	// make the config fail to load.
	daemon.mu.Lock()
	daemon.pastedSignIn = true
	daemon.mu.Unlock()
	reconnectExtension(t, mux, daemon)

	cfg := loadSavedConfig(t, configPath)
	pasted := cfg.ProviderInstances[providerInstanceIndex(cfg, "ext-pasted")]
	if pasted.State != config.ProviderInstanceStateDisabled || pasted.AuthConnectionRef != "" {
		t.Fatalf("ext-pasted after its credential changed = %+v", pasted)
	}
	if strings.Join(cfg.ActiveModels, ",") != "ext-free/free-model" || len(cfg.ModelRoutes) != 0 {
		t.Fatalf("after reconnect: active %#v, routes %#v", cfg.ActiveModels, cfg.ModelRoutes)
	}
	if cfg.Agents.Defaults.ModelName != "" || cfg.Agents.Defaults.ImageModel != "ext-free/free-model" {
		t.Fatalf("after reconnect: default %q, image %q", cfg.Agents.Defaults.ModelName, cfg.Agents.Defaults.ImageModel)
	}
}

func TestSignInCode(t *testing.T) {
	for _, tc := range []struct {
		input, state, want string
		wantErr            bool
	}{
		{input: "  raw-code ", want: "raw-code"},
		{input: "https://x.test/cb?code=c1&state=s", state: "s", want: "c1"},
		{input: "https://x.test/cb#code=c2&state=s", state: "s", want: "c2"},
		{input: "https://x.test/cb?code=c1&state=other", state: "s", wantErr: true},
		{input: "https://x.test/cb?error=access_denied", wantErr: true},
		{input: "", wantErr: true},
	} {
		got, err := signInCode(tc.input, tc.state)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("signInCode(%q) = %q, %v", tc.input, got, err)
		}
	}
}
