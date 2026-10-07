package modelservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	llmgwproviders "github.com/xibodev/llmgw-core/providers"

	"github.com/xibodev/compa/v3/pkg/config"
)

// anonymousUpstream is a fake anonymous provider: it serves catalog at
// /models and answers chat at chatPath with status and body, recording the
// model and path of every chat request.
type anonymousUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	chats []string
}

func newAnonymousUpstream(t *testing.T, catalog, chatPath string, status int, header http.Header, body string) *anonymousUpstream {
	t.Helper()
	upstream := &anonymousUpstream{}
	upstream.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, catalog)
		case r.Method == http.MethodPost:
			var request struct {
				Model string `json:"model"`
			}
			_ = json.NewDecoder(r.Body).Decode(&request)
			upstream.mu.Lock()
			upstream.chats = append(upstream.chats, r.URL.Path+" "+request.Model)
			upstream.mu.Unlock()
			if r.URL.Path != chatPath {
				http.NotFound(w, r)
				return
			}
			for name, values := range header {
				w.Header()[name] = values
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	return upstream
}

func (u *anonymousUpstream) chatRequests() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return slices.Clone(u.chats)
}

const anonymousAnswer = `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`

// llm7Catalog lists two free chat models, codestral-latest being the one
// core reviewed for LLM7.
const llm7Catalog = `{"data":[` +
	`{"id":"free-other","tier":"turbo","model_type":"chat","schema_endpoints":["openai"],"usage_based_only":false},` +
	`{"id":"codestral-latest","tier":"turbo","model_type":"chat","schema_endpoints":["openai"],"usage_based_only":false}]}`

// anonymousProfile returns the registry's anonymous profile of registryID,
// served at baseURL.
func anonymousProfile(t *testing.T, registryID, baseURL string) llmgwproviders.AnonymousProviderProfile {
	t.Helper()
	for _, profile := range llmgwproviders.AnonymousProviderProfiles() {
		if profile.RegistryID == registryID {
			profile.BaseURL = baseURL
			return profile
		}
	}
	t.Fatalf("no anonymous profile %q", registryID)
	return llmgwproviders.AnonymousProviderProfile{}
}

func TestVerifyAnonymousProviderTestsTheReviewedModelAndKeepsTheCatalog(t *testing.T) {
	upstream := newAnonymousUpstream(t, llm7Catalog, "/chat/completions", http.StatusOK, nil, anonymousAnswer)
	outcome := verifyAnonymousProvider(t.Context(), config.DefaultConfig(), anonymousProfile(t, "llm7", upstream.URL))

	if outcome.Status != anonymousVerified || outcome.ProbeModel != "codestral-latest" || outcome.Error != "" {
		t.Fatalf("outcome = %+v, want codestral-latest verified", outcome)
	}
	if got := upstream.chatRequests(); len(got) != 1 || got[0] != "/chat/completions codestral-latest" {
		t.Fatalf("chat requests = %v, want one test of the reviewed model", got)
	}
	if !slices.Equal(outcome.Models, []string{"free-other", "codestral-latest"}) || len(outcome.catalog) != 2 {
		t.Fatalf("models = %v, catalog = %+v; want the whole free catalog kept", outcome.Models, outcome.catalog)
	}
}

// A provider that lists only free models core did not review, such as a
// moderation model, is not tested and none of its models is enrolled.
func TestVerifyAnonymousProviderSkipsUnreviewedFreeModels(t *testing.T) {
	// A chat model admitted without a key, but not one core reviewed for the
	// test, and a safety classifier core does not admit at all.
	catalog := `{"data":[` +
		`{"id":"Llama-3.3-70B-Instruct","context_length":131072,"max_completion_tokens":16384,"pricing":{"prompt":"0.00000067","completion":"0.00000067"}},` +
		`{"id":"Guard-Classifier","context_length":32768,"max_completion_tokens":16384,"pricing":{"prompt":"0","completion":"0"}}]}`
	upstream := newAnonymousUpstream(t, catalog, "/chat/completions", http.StatusOK, nil, anonymousAnswer)
	outcome := verifyAnonymousProvider(t.Context(), config.DefaultConfig(), anonymousProfile(t, "ovh_ai_endpoints", upstream.URL))

	if outcome.Status != anonymousConnected || outcome.ErrorClass != anonymousNoModel || outcome.ProbeModel != "" {
		t.Fatalf("outcome = %+v, want no model to enroll", outcome)
	}
	if got := upstream.chatRequests(); len(got) != 0 {
		t.Fatalf("chat requests = %v, want none", got)
	}
	if !slices.Equal(outcome.Models, []string{"Llama-3.3-70B-Instruct"}) {
		t.Fatalf("models = %v, want the admitted chat model reported and no classifier", outcome.Models)
	}
}

// A refused test answer reports its class and when to retry, never the
// upstream's body.
func TestVerifyAnonymousProviderReportsARateLimitWithoutTheUpstreamBody(t *testing.T) {
	body := `{"error":{"message":"Rate limit exceeded","metadata":{"user_id":"org_secret_account"}}}`
	upstream := newAnonymousUpstream(t, llm7Catalog, "/chat/completions", http.StatusTooManyRequests, http.Header{"Retry-After": {"30"}}, body)
	outcome := verifyAnonymousProvider(t.Context(), config.DefaultConfig(), anonymousProfile(t, "llm7", upstream.URL))

	if outcome.Status != anonymousConnected || outcome.ErrorClass != anonymousRateLimited || outcome.ProbeModel != "codestral-latest" {
		t.Fatalf("outcome = %+v, want a rate-limited test", outcome)
	}
	if !strings.Contains(outcome.Error, "30 seconds") || strings.Contains(outcome.Error, "org_secret") || strings.Contains(outcome.Error, "{") {
		t.Fatalf("error = %q, want a short retry hint without the upstream body", outcome.Error)
	}
}

// Pollinations takes chat at /v1/chat/completions under a base without the
// version; the test answer goes through its vertical, so it reaches it.
func TestVerifyAnonymousProviderUsesPollinationsChatPath(t *testing.T) {
	catalog := `[{"name":"openai-fast","tier":"anonymous","output_modalities":["text"]}]`
	upstream := newAnonymousUpstream(t, catalog, "/v1/chat/completions", http.StatusOK, nil, anonymousAnswer)
	outcome := verifyAnonymousProvider(t.Context(), config.DefaultConfig(), anonymousProfile(t, "pollinations", upstream.URL))

	if outcome.Status != anonymousVerified || outcome.ProbeModel != "openai-fast" {
		t.Fatalf("outcome = %+v, chat requests = %v; want openai-fast verified", outcome, upstream.chatRequests())
	}
	if got := upstream.chatRequests(); len(got) != 1 || got[0] != "/v1/chat/completions openai-fast" {
		t.Fatalf("chat requests = %v, want Pollinations' chat path", got)
	}
}

// An instance the owner set up under the profile's id with another endpoint
// is left alone and its provider is not contacted.
func TestVerifyAnonymousProviderLeavesACollidingInstanceAlone(t *testing.T) {
	upstream := newAnonymousUpstream(t, llm7Catalog, "/chat/completions", http.StatusOK, nil, anonymousAnswer)
	profile := anonymousProfile(t, "llm7", upstream.URL)
	cfg := config.DefaultConfig()
	cfg.ProviderInstances = append(cfg.ProviderInstances, &config.ProviderInstanceConfig{
		ID: profile.ProviderID, ProviderKind: "llm7", Adapter: config.ProviderAdapterOpenAICompatible,
		Protocol: "openai", Endpoint: "https://mine.example.test/v1", State: config.ProviderInstanceStateEnabled,
	})
	outcome := verifyAnonymousProvider(t.Context(), cfg, profile)

	if outcome.Status != anonymousFailed || !strings.Contains(outcome.Error, "left alone") {
		t.Fatalf("outcome = %+v, want the colliding instance left alone", outcome)
	}
	if got := upstream.chatRequests(); len(got) != 0 {
		t.Fatalf("chat requests = %v, want none", got)
	}
}

// Enrolling a provider saves the free catalog it listed, while only the
// model that answered joins Chat.
func TestAutoConnectFreeSavesTheListedCatalog(t *testing.T) {
	t.Setenv("COMPA_HOME", t.TempDir())
	cfg := config.DefaultConfig()
	result, err := AutoConnectFree(context.Background(), cfg, func(context.Context, *config.Config) []AnonymousProviderOutcome {
		return []AnonymousProviderOutcome{{
			RegistryID: "llm7", ProviderID: "llm7", Status: anonymousVerified,
			Models: []string{"free-other", "codestral-latest"}, ProbeModel: "codestral-latest",
			catalog: []CatalogModel{{ID: "free-other", DisplayName: "Other"}, {ID: "codestral-latest"}},
		}}
	})
	if err != nil || result.Verified != 1 {
		t.Fatalf("AutoConnectFree() = %+v, %v", result, err)
	}
	if !slices.Equal(cfg.ActiveModels, []string{"llm7/codestral-latest"}) {
		t.Fatalf("active models = %v, want only the model that answered", cfg.ActiveModels)
	}
	store, err := LoadCatalogs()
	if err != nil {
		t.Fatal(err)
	}
	entry := store.Entries["llm7"]
	if entry == nil || len(entry.Models) != 2 || entry.Models[0].DisplayName != "Other" {
		t.Fatalf("saved catalog = %+v, want the listed free models", entry)
	}
}
