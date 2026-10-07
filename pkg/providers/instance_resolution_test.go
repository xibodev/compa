package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/xibodev/llmgw-core"

	"github.com/xibodev/compa/v3/pkg/config"
)

type instanceResolutionProvider struct {
	instanceID string
	endpoint   string
	secret     string
	headers    map[string]string
}

func (p *instanceResolutionProvider) Chat(context.Context, []Message, []ToolDefinition, string, map[string]any) (*LLMResponse, error) {
	return nil, errors.New("not called")
}

func (p *instanceResolutionProvider) GetDefaultModel() string { return "" }

func instanceResolutionFixture(id, endpoint string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID:                id,
		ProviderKind:      "openai",
		Adapter:           "openai-compatible",
		Protocol:          "openai",
		Endpoint:          endpoint,
		AuthConnectionRef: "credential:" + id,
		Headers:           map[string]string{"X-Instance": id},
		State:             config.ProviderInstanceStateEnabled,
	}
}

func recordingInstanceFactory(calls *[]*instanceResolutionProvider) InstanceProviderFactory {
	return func(instance *config.ProviderInstanceConfig, modelID string, secret string) (LLMProvider, error) {
		provider := &instanceResolutionProvider{
			instanceID: instance.ID,
			endpoint:   instance.Endpoint,
			secret:     secret,
			headers:    cloneStringMap(instance.Headers),
		}
		*calls = append(*calls, provider)
		return provider, nil
	}
}

func fixtureCredentialResolver(ref string) (string, error) {
	return strings.TrimPrefix(ref, "credential:") + "-secret", nil
}

func TestResolveInstanceTargetDirect(t *testing.T) {
	instance := instanceResolutionFixture("primary", "https://primary.example.test/v1")
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{instance}}
	catalogs := map[string]InstanceCatalog{"primary": {InstanceID: "primary", Models: []string{"org/model"}}}
	var calls []*instanceResolutionProvider

	resolved, err := ResolveInstanceTargetOrRoute(
		cfg,
		catalogs,
		"primary/org/model",
		fixtureCredentialResolver,
		recordingInstanceFactory(&calls),
	)
	if err != nil {
		t.Fatalf("ResolveInstanceTargetOrRoute() error = %v", err)
	}
	if len(resolved.Candidates) != 1 {
		t.Fatalf("candidates = %#v", resolved.Candidates)
	}
	candidate := resolved.Candidates[0]
	if candidate.Provider != "openai" || candidate.Model != "org/model" || candidate.IdentityKey != "provider_instance:primary" {
		t.Fatalf("candidate = %#v", candidate)
	}
	provider, err := resolved.ProviderForCandidate(candidate)
	if err != nil || provider != calls[0] {
		t.Fatal("candidate provider did not retain exact instance ownership")
	}
}

func TestResolveInstanceRoutePreservesOrderAndOwnership(t *testing.T) {
	first := instanceResolutionFixture("first", "https://first.example.test/v1")
	second := instanceResolutionFixture("second", "https://second.example.test/v1")
	cfg := &config.Config{
		ProviderInstances: []*config.ProviderInstanceConfig{first, second},
		ModelRoutes: []*config.ModelRouteConfig{{
			Name:    "chat-route",
			Targets: []string{"second/shared-model", "first/shared-model"},
		}},
	}
	catalogs := map[string]InstanceCatalog{
		"first":  {InstanceID: "first", Models: []string{"shared-model"}},
		"second": {InstanceID: "second", Models: []string{"shared-model"}},
	}
	var calls []*instanceResolutionProvider

	resolved, err := ResolveInstanceTargetOrRoute(
		cfg,
		catalogs,
		"chat-route",
		fixtureCredentialResolver,
		recordingInstanceFactory(&calls),
	)
	if err != nil {
		t.Fatalf("ResolveInstanceTargetOrRoute() error = %v", err)
	}
	if len(resolved.Candidates) != 2 || resolved.Candidates[0].DisplayName != "second/shared-model" || resolved.Candidates[1].DisplayName != "first/shared-model" {
		t.Fatalf("ordered candidates = %#v", resolved.Candidates)
	}
	if resolved.Candidates[0].StableKey() == resolved.Candidates[1].StableKey() {
		t.Fatalf("same-kind identical models share stable key: %#v", resolved.Candidates)
	}
	firstResolved, err := resolved.ProviderForCandidate(resolved.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	secondResolved, err := resolved.ProviderForCandidate(resolved.Candidates[1])
	if err != nil {
		t.Fatal(err)
	}
	firstProvider := firstResolved.(*instanceResolutionProvider)
	secondProvider := secondResolved.(*instanceResolutionProvider)
	if firstProvider.instanceID != "second" || firstProvider.endpoint != second.Endpoint || firstProvider.secret != "second-secret" || firstProvider.headers["X-Instance"] != "second" {
		t.Fatalf("first route provider borrowed ownership: %#v", firstProvider)
	}
	if secondProvider.instanceID != "first" || secondProvider.endpoint != first.Endpoint || secondProvider.secret != "first-secret" || secondProvider.headers["X-Instance"] != "first" {
		t.Fatalf("second route provider borrowed ownership: %#v", secondProvider)
	}
}

func TestResolveInstanceTargetValidationFailures(t *testing.T) {
	enabled := instanceResolutionFixture("enabled", "https://enabled.example.test/v1")
	disabled := instanceResolutionFixture("disabled", "https://disabled.example.test/v1")
	disabled.State = config.ProviderInstanceStateDisabled
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{enabled, disabled}}
	validCatalog := InstanceCatalog{InstanceID: "enabled", Models: []string{"model"}}

	tests := []struct {
		name      string
		selection string
		catalogs  map[string]InstanceCatalog
		wantErr   string
	}{
		{name: "missing instance", selection: "missing/model", catalogs: map[string]InstanceCatalog{}, wantErr: `instance "missing" not found`},
		{name: "disabled instance", selection: "disabled/model", catalogs: map[string]InstanceCatalog{"disabled": {InstanceID: "disabled", Models: []string{"model"}}}, wantErr: "is disabled"},
		{name: "missing catalog", selection: "enabled/model", catalogs: map[string]InstanceCatalog{}, wantErr: "catalog"},
		{name: "catalog belongs to another instance", selection: "enabled/model", catalogs: map[string]InstanceCatalog{"enabled": {InstanceID: "other", Models: []string{"model"}}}, wantErr: "catalog"},
		{name: "missing model", selection: "enabled/other", catalogs: map[string]InstanceCatalog{"enabled": validCatalog}, wantErr: "not found"},
		{name: "missing route", selection: "not-a-route", catalogs: map[string]InstanceCatalog{"enabled": validCatalog}, wantErr: "route was not found"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			_, err := ResolveInstanceTargetOrRoute(
				cfg,
				tc.catalogs,
				tc.selection,
				fixtureCredentialResolver,
				func(*config.ProviderInstanceConfig, string, string) (LLMProvider, error) {
					called = true
					return &instanceResolutionProvider{}, nil
				},
			)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
			}
			if called {
				t.Fatal("provider factory called for invalid target")
			}
		})
	}
}

func TestCreateProviderFromInstanceRejectsUnsupportedAdapter(t *testing.T) {
	_, err := CreateProviderFromInstance(&config.ProviderInstanceConfig{
		ID: "native", Adapter: "native-sdk", Protocol: "native",
	}, "model", "secret")
	if err == nil {
		t.Fatal("CreateProviderFromInstance() error = nil, want unsupported adapter error")
	}
}

func boolPtr(value bool) *bool { return &value }

func tunedRuntime() *config.ProviderInstanceRuntime {
	return &config.ProviderInstanceRuntime{
		Proxy:               "http://proxy.example.test:8080",
		RequestTimeout:      45,
		RPM:                 12,
		Streaming:           boolPtr(true),
		ThinkingLevel:       "high",
		MaxTokensField:      "max_completion_tokens",
		ToolSchemaTransform: "simple",
		ExtraBody:           map[string]any{"reasoning_split": true},
	}
}

func TestResolveInstanceTargetCarriesRuntime(t *testing.T) {
	instance := instanceResolutionFixture("tuned", "https://tuned.example.test/v1")
	instance.Runtime = tunedRuntime()
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{instance}}
	resolved, err := ResolveInstanceTargetOrRoute(
		cfg,
		map[string]InstanceCatalog{"tuned": {InstanceID: "tuned", Models: []string{"model"}}},
		"tuned/model",
		fixtureCredentialResolver,
		nil,
	)
	if err != nil {
		t.Fatalf("ResolveInstanceTargetOrRoute() error = %v", err)
	}
	candidate := resolved.Candidates[0]
	if candidate.RPM != 12 {
		t.Fatalf("candidate RPM = %d, want the instance's runtime rpm", candidate.RPM)
	}

	// The resolution owns a copy: later edits to the config do not reach it.
	instance.Runtime.RPM = 99
	instance.Runtime.ExtraBody["reasoning_split"] = false
	*instance.Runtime.Streaming = false

	spec, err := resolved.CallSpecForCandidate(candidate)
	if err != nil {
		t.Fatalf("CallSpecForCandidate() error = %v", err)
	}
	if spec.DisplayName != "tuned/model" || spec.ModelID != "model" {
		t.Fatalf("call spec identity = %#v", spec)
	}
	if spec.RPM != 12 || !spec.Streaming || spec.ThinkingLevel != "high" || spec.MaxTokensField != "max_completion_tokens" ||
		spec.ToolSchemaTransform != "simple" || spec.ExtraBody["reasoning_split"] != true {
		t.Fatalf("call spec runtime = %#v", spec)
	}

	// A provider adding fields to its body works on its own copy.
	spec.ExtraBody["added"] = true
	again, _ := resolved.CallSpecForCandidate(candidate)
	if _, leaked := again.ExtraBody["added"]; leaked {
		t.Fatal("CallSpecForCandidate() shares extra_body between calls")
	}
}
func TestResolveInstanceTargetWithoutRuntimeUsesDefaults(t *testing.T) {
	cfg := &config.Config{ProviderInstances: []*config.ProviderInstanceConfig{instanceResolutionFixture("plain", "https://plain.example.test/v1")}}
	resolved, err := ResolveInstanceTargetOrRoute(cfg, map[string]InstanceCatalog{"plain": {InstanceID: "plain", Models: []string{"m"}}}, "plain/m", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := resolved.CallSpecForCandidate(resolved.Candidates[0])
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Candidates[0].RPM != 0 || !spec.Streaming || spec.ThinkingLevel != "" || spec.ExtraBody != nil {
		t.Fatalf("an instance without runtime settings resolved to %#v (rpm %d); want the defaults, streaming on", spec, resolved.Candidates[0].RPM)
	}
}

// recordedRequest is what a fixture endpoint received.
type recordedRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

// recordingEndpoint serves response to every request and returns what it
// received so far.
func recordingEndpoint(t *testing.T, response string) (*httptest.Server, func() []recordedRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []recordedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		requests = append(requests, recordedRequest{path: r.URL.Path, header: r.Header.Clone(), body: body})
		mu.Unlock()
		if strings.HasPrefix(response, "event:") || strings.HasPrefix(response, "data:") {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server, func() []recordedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedRequest(nil), requests...)
	}
}

func isToolSchemaTransformWrapper(provider LLMProvider) bool {
	switch provider.(type) {
	case *toolSchemaTransformProvider, *toolSchemaStreamingProvider:
		return true
	}
	return false
}

func TestCreateProviderFromInstanceAppliesRuntime(t *testing.T) {
	server, requests := recordingEndpoint(t, openaiCompatResponse)
	instance := instanceResolutionFixture("tuned", server.URL+"/v1")
	instance.Runtime = tunedRuntime()
	instance.Runtime.Proxy = ""

	provider, err := CreateProviderFromInstance(instance, "gpt-test", "instance-secret")
	if err != nil {
		t.Fatalf("CreateProviderFromInstance() error = %v", err)
	}
	if !isToolSchemaTransformWrapper(provider) {
		t.Fatalf("provider = %T, want the tool schema transform the runtime asks for", provider)
	}
	if _, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "gpt-test", map[string]any{"max_tokens": 100}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	received := requests()
	if len(received) != 1 {
		t.Fatalf("requests = %d, want 1", len(received))
	}
	got := received[0]
	if got.header.Get("Authorization") != "Bearer instance-secret" || got.header.Get("X-Instance") != "tuned" {
		t.Fatalf("headers = %v", got.header)
	}
	if got.body["max_completion_tokens"] != float64(100) || got.body["reasoning_split"] != true {
		t.Fatalf("body = %v, want max_tokens_field and extra_body applied", got.body)
	}
}

// extensionDaemonFixture serves an extension daemon's operations, answering
// each with response, and replaces the daemon secret for the test.
func extensionDaemonFixture(t *testing.T, response string) (*config.ProviderInstanceConfig, func() []recordedRequest) {
	t.Helper()
	server, requests := recordingEndpoint(t, response)
	original := ExtensionDaemonSecret
	ExtensionDaemonSecret = func() (string, error) { return "daemon-secret", nil }
	t.Cleanup(func() { ExtensionDaemonSecret = original })
	return &config.ProviderInstanceConfig{
		ID:           "ext-acme",
		ProviderKind: "acme",
		Adapter:      config.ProviderAdapterExtension,
		Protocol:     config.ExtensionSurfaceChatCompletions,
		Endpoint:     server.URL,
		Headers:      map[string]string{"X-Team": "core", "Authorization": "Bearer spoofed"},
		Settings: map[string]any{
			config.ExtensionProviderSetting:   "acme",
			config.ExtensionCredentialSetting: "none",
		},
		State: config.ProviderInstanceStateEnabled,
	}, requests
}

// An extension instance's Chat streams, since a daemon's plain answer can
// lack the tool calls its stream carries (see CreateProviderFromInstance).
func TestCreateProviderFromInstanceExtensionAppliesRuntimeAndHeaders(t *testing.T) {
	instance, requests := extensionDaemonFixture(t, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	instance.Runtime = &config.ProviderInstanceRuntime{
		RequestTimeout:      30,
		MaxTokensField:      "max_completion_tokens",
		ToolSchemaTransform: "simple",
		ExtraBody:           map[string]any{"reasoning_split": true},
	}

	provider, err := CreateProviderFromInstance(instance, "acme-model", "")
	if err != nil {
		t.Fatalf("CreateProviderFromInstance() error = %v", err)
	}
	if !isToolSchemaTransformWrapper(provider) {
		t.Fatalf("provider = %T, want the tool schema transform the runtime asks for", provider)
	}
	answer, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "acme-model", map[string]any{"max_tokens": 64})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if answer.Content != "ok" {
		t.Fatalf("Chat() content = %q, want the streamed answer", answer.Content)
	}
	received := requests()
	if len(received) != 1 {
		t.Fatalf("daemon requests = %d, want 1", len(received))
	}
	got := received[0]
	if !strings.HasSuffix(got.path, "/acme/stream") || got.body["stream"] != true {
		t.Fatalf("path = %q, stream = %v; want the daemon's stream operation", got.path, got.body["stream"])
	}
	if got.header.Get("X-Team") != "core" {
		t.Fatalf("instance header did not reach the daemon: %v", got.header)
	}
	if got.header.Get("Authorization") != "Bearer daemon-secret" {
		t.Fatalf("Authorization = %q, want the daemon secret, not the instance header", got.header.Get("Authorization"))
	}
	if got.body["max_completion_tokens"] != float64(64) || got.body["reasoning_split"] != true {
		t.Fatalf("body = %v, want max_tokens_field and extra_body applied", got.body)
	}
}

func TestCreateProviderFromInstanceExtensionMessagesSendsHeaders(t *testing.T) {
	// Every extension chat streams, so the daemon answers with a stream.
	instance, requests := extensionDaemonFixture(t, anthropicStreamResponse)
	instance.Protocol = config.ExtensionSurfaceMessages

	provider, err := CreateProviderFromInstance(instance, "acme-model", "")
	if err != nil {
		t.Fatalf("CreateProviderFromInstance() error = %v", err)
	}
	if isToolSchemaTransformWrapper(provider) {
		t.Fatalf("provider = %T, want no tool schema transform without runtime settings", provider)
	}
	answer, err := provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "acme-model", map[string]any{"max_tokens": 64})
	if err != nil || answer.Content != "ok" {
		t.Fatalf("Chat() = %#v, %v; want the streamed answer", answer, err)
	}
	received := requests()
	if len(received) != 1 {
		t.Fatalf("daemon requests = %d, want 1", len(received))
	}
	if got := received[0].header.Get("X-Team"); got != "core" {
		t.Fatalf("X-Team = %q, want the instance header", got)
	}
}

func TestCreateProviderFromInstanceExtensionHonoursRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(1500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, openaiCompatResponse)
	}))
	defer server.Close()
	instance, _ := extensionDaemonFixture(t, openaiCompatResponse)
	instance.Endpoint = server.URL
	instance.Runtime = &config.ProviderInstanceRuntime{RequestTimeout: 1}

	provider, err := CreateProviderFromInstance(instance, "acme-model", "")
	if err != nil {
		t.Fatalf("CreateProviderFromInstance() error = %v", err)
	}
	_, err = provider.Chat(t.Context(), []Message{{Role: "user", Content: "hi"}}, nil, "acme-model", nil)
	if failure := DescribeFailure(err); err == nil || failure.Class != core.ProviderErrorTransport || failure.Disposition != core.DispositionRetryable {
		t.Fatalf("Chat() error = %v failure = %+v, want the runtime request timeout as a retryable transport failure", err, failure)
	}
}
