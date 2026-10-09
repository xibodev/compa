package modelservice

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	core "github.com/xibodev/llmgw-core"
	"github.com/xibodev/llmgw-core/anonymous"
	llmgwproviders "github.com/xibodev/llmgw-core/providers"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/providers"
)

// AnonymousVerifyFunc checks the free providers that need no key against cfg,
// which it reads but never changes, and reports each one's outcome.
type AnonymousVerifyFunc func(ctx context.Context, cfg *config.Config) []AnonymousProviderOutcome

// Outcome statuses: a provider whose test answer came back, one that listed
// models but whose test failed or that offered no model to test, and one
// that could not be checked at all.
const (
	anonymousVerified  = "verified"
	anonymousConnected = "connected"
	anonymousFailed    = "failed"
)

// Outcome error classes, which the UI words.
const (
	anonymousRateLimited  = "rate_limited"
	anonymousAuthRequired = "auth_required"
	anonymousForbidden    = "forbidden"
	anonymousProbeFailed  = "probe_failed"
	anonymousNoModel      = "no_model"
)

// Time one provider's catalog read and its test answer may each take, so a
// provider that hangs cannot hold up the free provider test.
const (
	anonymousDiscoverTimeout = 20 * time.Second
	anonymousProbeTimeout    = 30 * time.Second
)

// verifyAnonymousProviders checks every anonymous profile of core's registry
// with core's anonymous.Orchestrator, each on its own at once. Catalogs and
// the test answer go through the provider instance's own core provider, so
// each provider is read and invoked by its vertical (Pollinations' chat path
// included), and failures carry only their class, never the upstream's body.
func verifyAnonymousProviders(ctx context.Context, cfg *config.Config) []AnonymousProviderOutcome {
	profiles := llmgwproviders.AnonymousProviderProfiles()
	outcomes := make([]AnonymousProviderOutcome, len(profiles))
	var wg sync.WaitGroup
	for i, profile := range profiles {
		wg.Add(1)
		go func() {
			defer wg.Done()
			outcomes[i] = verifyAnonymousProvider(ctx, cfg, profile)
		}()
	}
	wg.Wait()
	return outcomes
}

func verifyAnonymousProvider(ctx context.Context, cfg *config.Config, profile llmgwproviders.AnonymousProviderProfile) AnonymousProviderOutcome {
	check := &anonymousCheck{cfg: cfg, profile: profile}
	orchestrator, err := anonymous.New(anonymous.Options{
		Profiles: []llmgwproviders.AnonymousProviderProfile{profile},
		Catalog:  anonymous.CatalogFunc(check.discover),
		Invoker:  anonymous.InvokerFunc(check.invoke),
		Hooks:    check,
		Probe:    anonymous.ProbeVerificationModel,
	})
	if err != nil {
		return AnonymousProviderOutcome{
			RegistryID: profile.RegistryID, ProviderID: profile.ProviderID,
			Status: anonymousFailed, Error: "Compa could not check this provider.",
		}
	}
	results := orchestrator.ConnectAll(ctx)
	if len(results) == 0 {
		return AnonymousProviderOutcome{
			RegistryID: profile.RegistryID, ProviderID: profile.ProviderID,
			Status: anonymousFailed, Error: "The check stopped before it reached this provider.",
		}
	}
	return check.outcome(results[0])
}

// anonymousCheck is one provider's check: the instance it runs on, built
// like the one the free provider test enrolls, and what its catalog listed.
// It is the orchestrator's hooks; nothing it does is kept, since the test
// enrolls the providers that passed only once every check is done.
type anonymousCheck struct {
	cfg     *config.Config
	profile llmgwproviders.AnonymousProviderProfile

	mu       sync.Mutex
	instance *config.ProviderInstanceConfig
	provider core.Provider
	catalog  []CatalogModel
}

// Statuses Enroll gives a provider the check leaves alone.
const (
	enrollCollision = "collision"
	enrollDisabled  = "disabled"
)

func (c *anonymousCheck) Enabled(context.Context) bool { return true }

// Enroll checks the instance the owner already has under the profile's id,
// with its own settings, and otherwise the one the test would create. An
// instance set up otherwise, or turned off, is left alone.
func (c *anonymousCheck) Enroll(_ context.Context, profile llmgwproviders.AnonymousProviderProfile) (string, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	instance := anonymousInstance(profile)
	if existing := configuredInstance(c.cfg, instance.ID); existing != nil {
		if !sameAnonymousInstance(existing, instance) {
			return instance.ID, enrollCollision
		}
		if existing.State == config.ProviderInstanceStateDisabled {
			return instance.ID, enrollDisabled
		}
		instance = existing
	}
	c.instance = instance
	return instance.ID, anonymous.StatusManaged
}

func (c *anonymousCheck) Claim(context.Context, string, time.Time, time.Duration) (bool, error) {
	return true, nil
}

func (c *anonymousCheck) Generation(context.Context, string) (int64, error) { return 1, nil }

func (c *anonymousCheck) Record(context.Context, string, int64, anonymous.Result) error { return nil }

// coreProvider returns the core provider of the checked instance.
func (c *anonymousCheck) coreProvider() (core.Provider, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.instance == nil {
		return nil, fmt.Errorf("provider %q is not enrolled", c.profile.RegistryID)
	}
	if c.provider == nil {
		provider, err := providers.NewCoreProvider(c.instance)
		if err != nil {
			return nil, err
		}
		c.provider = provider
	}
	return c.provider, nil
}

// discover lists the free models the provider admits without a key, and
// keeps them, and offers the check only the ones core reviewed for this
// provider, most preferred first: a model the review did not cover, such as
// a moderation model a provider happens to list for free, is never added to
// Chat on its own.
func (c *anonymousCheck) discover(ctx context.Context, _ core.Caller, _ string) ([]core.ModelInfo, error) {
	provider, err := c.coreProvider()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, anonymousDiscoverTimeout)
	defer cancel()
	models, err := provider.ListModels(ctx, nil)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.catalog = CatalogModelsFromCore(models)
	c.mu.Unlock()
	return reviewedModels(c.profile.VerificationModels, models), nil
}

func (c *anonymousCheck) invoke(ctx context.Context, _ core.Caller, _ string, request core.Request) (core.Response, error) {
	provider, err := c.coreProvider()
	if err != nil {
		return core.Response{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, anonymousProbeTimeout)
	defer cancel()
	return provider.Invoke(ctx, request)
}

// reviewedModels returns the models among listed whose id is one of
// reviewed, in reviewed's order, ignoring case.
func reviewedModels(reviewed []string, listed []core.ModelInfo) []core.ModelInfo {
	byID := make(map[string]core.ModelInfo, len(listed))
	for _, model := range listed {
		byID[strings.ToLower(strings.TrimSpace(model.ID))] = model
	}
	out := make([]core.ModelInfo, 0, len(reviewed))
	for _, id := range reviewed {
		if model, ok := byID[strings.ToLower(id)]; ok {
			out = append(out, model)
		}
	}
	return out
}

// outcome reports result as the free provider test shows it. Its error is a
// short sentence and never the upstream's answer.
func (c *anonymousCheck) outcome(result anonymous.Result) AnonymousProviderOutcome {
	c.mu.Lock()
	catalog := slices.Clone(c.catalog)
	c.mu.Unlock()
	outcome := AnonymousProviderOutcome{
		RegistryID: c.profile.RegistryID, ProviderID: result.ProviderID, catalog: catalog,
	}
	if outcome.ProviderID == "" {
		outcome.ProviderID = c.profile.ProviderID
	}
	for _, model := range catalog {
		outcome.Models = append(outcome.Models, model.ID)
	}
	for _, probe := range result.Connect.Probes {
		if outcome.ProbeModel == "" || probe.Status == core.CompletionVerified {
			outcome.ProbeModel, outcome.LatencyMS = probe.Target.Model, probe.Latency.Milliseconds()
		}
		if probe.Status == core.CompletionVerified {
			break
		}
	}
	switch {
	case result.Status == anonymous.StatusPassed:
		outcome.Status = anonymousVerified
	case result.Status == enrollCollision:
		outcome.Status = anonymousFailed
		outcome.Error = "A provider with this name is already set up differently, so it was left alone."
	case result.Status == enrollDisabled:
		outcome.Status = anonymousFailed
		outcome.Error = "It is turned off in your providers."
	case result.FailureCode == anonymous.FailureModelUnavailable:
		outcome.Status, outcome.ErrorClass = anonymousConnected, anonymousNoModel
		outcome.Error = "It lists no free chat model Compa adds on its own; you can still add its models by hand."
	case result.FailureCode == anonymous.FailureVerificationFailed || result.Probed > 0:
		outcome.Status, outcome.ErrorClass = anonymousConnected, anonymousErrorClass(result.Connect.Health.ErrorClass)
		outcome.Error = anonymousProbeError(result.Connect.Health)
	default:
		outcome.Status, outcome.ErrorClass = anonymousFailed, anonymousErrorClass(result.Connect.Health.ErrorClass)
		outcome.Error = "Its model list could not be read."
		if outcome.ErrorClass == anonymousAuthRequired || outcome.ErrorClass == anonymousForbidden {
			outcome.Error = "It no longer answers without an API key."
		}
	}
	return outcome
}

func anonymousErrorClass(class core.ProviderErrorClass) string {
	switch class {
	case core.ProviderErrorRateLimited:
		return anonymousRateLimited
	case core.ProviderErrorAuth:
		return anonymousAuthRequired
	case core.ProviderErrorForbidden:
		return anonymousForbidden
	}
	return anonymousProbeFailed
}

func anonymousProbeError(health core.ProviderHealthEvidence) string {
	switch health.ErrorClass {
	case core.ProviderErrorRateLimited:
		if seconds := int(health.RetryAfter.Round(time.Second) / time.Second); seconds > 0 {
			return fmt.Sprintf("It is busy right now; try again in %d seconds.", seconds)
		}
		return "It is busy right now; try again in a minute."
	case core.ProviderErrorAuth, core.ProviderErrorForbidden:
		return "It refused the test message without an API key."
	case core.ProviderErrorTransport:
		return "It could not be reached."
	}
	return "Its test answer failed."
}

// anonymousInstance returns the provider instance the free provider test
// enrolls for profile.
func anonymousInstance(profile llmgwproviders.AnonymousProviderProfile) *config.ProviderInstanceConfig {
	id := strings.TrimSpace(profile.ProviderID)
	if id == "" {
		id = profile.RegistryID
	}
	return &config.ProviderInstanceConfig{
		ID:           id,
		ProviderKind: profile.RegistryID,
		Adapter:      config.ProviderAdapterOpenAICompatible,
		Protocol:     "openai",
		Endpoint:     profile.BaseURL,
		State:        config.ProviderInstanceStateEnabled,
	}
}

// sameAnonymousInstance reports whether existing is the instance the test
// enrolls as want: the same provider, adapter and endpoint.
func sameAnonymousInstance(existing, want *config.ProviderInstanceConfig) bool {
	return existing.ProviderKind == want.ProviderKind && existing.Adapter == want.Adapter &&
		existing.Protocol == want.Protocol &&
		strings.TrimRight(existing.Endpoint, "/") == strings.TrimRight(want.Endpoint, "/")
}

func configuredInstance(cfg *config.Config, id string) *config.ProviderInstanceConfig {
	if cfg == nil {
		return nil
	}
	for _, instance := range cfg.ProviderInstances {
		if instance != nil && instance.ID == id {
			return instance
		}
	}
	return nil
}

// AutoConnectFree checks the free providers that need no key and, for each whose
// test answer came back, enrolls its instance with the free models its
// catalog lists and adds the model that answered to Chat. The first model
// added becomes the default model when none is set. An instance the owner
// set up otherwise is never changed.
func AutoConnectFree(ctx context.Context, cfg *config.Config, verify AnonymousVerifyFunc) (*AutoConnectResult, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}
	if verify == nil {
		verify = verifyAnonymousProviders
	}
	profiles := llmgwproviders.AnonymousProviderProfiles()
	profilesByRegistry := make(map[string]llmgwproviders.AnonymousProviderProfile, len(profiles))
	for _, profile := range profiles {
		profilesByRegistry[profile.RegistryID] = profile
	}

	verified := 0
	catalogDiscovered := 0
	var connectedInstances []string
	var firstVerified string
	outcomes := verify(ctx, cfg)

	for _, outcome := range outcomes {
		if len(outcome.Models) > 0 {
			catalogDiscovered++
		}
		probe := strings.TrimSpace(outcome.ProbeModel)
		if outcome.Status != anonymousVerified || probe == "" {
			continue
		}
		profile, ok := profilesByRegistry[outcome.RegistryID]
		if !ok {
			continue
		}
		want := anonymousInstance(profile)
		if id := strings.TrimSpace(outcome.ProviderID); id != "" {
			want.ID = id
		}
		instance := configuredInstance(cfg, want.ID)
		isNew := instance == nil
		if isNew {
			instance = want
		} else if !sameAnonymousInstance(instance, want) || instance.State == config.ProviderInstanceStateDisabled {
			continue
		}
		if err := SaveProviderInstanceCatalog(instance, outcome.enrolledCatalog(probe)); err != nil {
			continue
		}
		if isNew {
			cfg.ProviderInstances = append(cfg.ProviderInstances, instance)
			connectedInstances = append(connectedInstances, instance.ID)
		}
		verified++
		exact := instance.ID + "/" + probe
		if firstVerified == "" {
			firstVerified = exact
		}
		if !containsTrimmed(cfg.ActiveModels, exact) {
			cfg.ActiveModels = append(cfg.ActiveModels, exact)
		}
	}

	result := &AutoConnectResult{
		OK:                verified > 0,
		Total:             len(profiles),
		CatalogDiscovered: catalogDiscovered,
		Verified:          verified,
		Instances:         connectedInstances,
		Outcomes:          outcomes,
	}
	// Chat works right away on the first model that answered, unless the
	// owner already chose a default.
	if AdoptDefaultModel(cfg, firstVerified) {
		result.DefaultModel = firstVerified
	}
	return result, nil
}

// enrolledCatalog is the catalog the test saves for an outcome's instance:
// the free models its catalog listed, or their ids, and always the model
// that answered.
func (o AnonymousProviderOutcome) enrolledCatalog(probe string) []CatalogModel {
	catalog := slices.Clone(o.catalog)
	if len(catalog) == 0 {
		for _, id := range o.Models {
			if id = strings.TrimSpace(id); id != "" {
				catalog = append(catalog, CatalogModel{ID: id})
			}
		}
	}
	if !slices.ContainsFunc(catalog, func(model CatalogModel) bool { return model.ID == probe }) {
		catalog = append([]CatalogModel{{ID: probe}}, catalog...)
	}
	return catalog
}

// AdoptDefaultModel makes target, a chat model just verified or added to the
// chat shortlist, the default model when no default is set, and reports
// whether it did. A default already set is never replaced.
func AdoptDefaultModel(cfg *config.Config, target string) bool {
	target = strings.TrimSpace(target)
	if cfg == nil || target == "" || strings.TrimSpace(cfg.Agents.Defaults.ModelName) != "" {
		return false
	}
	cfg.Agents.Defaults.ModelName = target
	return true
}

// AutoConnectFreeAndSave connects free providers and saves the updated configuration to disk.
func AutoConnectFreeAndSave(ctx context.Context, configPath string, verify AnonymousVerifyFunc) (*AutoConnectResult, error) {
	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	res, err := AutoConnectFree(ctx, cfg, verify)
	if err != nil {
		return nil, err
	}

	if res.Verified > 0 {
		if err := config.SaveConfig(configPath, cfg); err != nil {
			return nil, fmt.Errorf("save config: %w", err)
		}
	}

	return res, nil
}

func containsTrimmed(values []string, want string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}
