package modelservice

import (
	"errors"
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/auth"
	"github.com/xibodev/compa/pkg/config"
)

func voiceConfig(instances ...*config.ProviderInstanceConfig) *config.Config {
	return &config.Config{ProviderInstances: instances}
}

func voiceInstance(id, kind string) *config.ProviderInstanceConfig {
	return &config.ProviderInstanceConfig{
		ID: id, ProviderKind: kind, Adapter: config.ProviderAdapterOpenAICompatible, Protocol: "openai",
		Endpoint: "https://" + id + ".example.test/v1/", AuthConnectionRef: "credential:" + id,
		State: config.ProviderInstanceStateEnabled,
	}
}

func stubVoiceCredentials(secret string) *Resolver {
	return NewResolver(WithCredentialResolver(func(ref string) (string, error) {
		return secret + ":" + ref, nil
	}))
}

func TestResolveVoiceTargetReturnsTheInstanceModelAndSecret(t *testing.T) {
	instance := voiceInstance("voice", "elevenlabs")
	got, err := stubVoiceCredentials("secret").ResolveVoiceTarget(voiceConfig(instance), " voice/scribe_v2 ")
	if err != nil {
		t.Fatalf("ResolveVoiceTarget() error = %v", err)
	}
	if got.Instance != instance || got.Model != "scribe_v2" || got.Secret != "secret:credential:voice" {
		t.Fatalf("ResolveVoiceTarget() = %#v", got)
	}

	keyless := voiceInstance("local", "openai")
	keyless.AuthConnectionRef = ""
	got, err = stubVoiceCredentials("unused").ResolveVoiceTarget(voiceConfig(keyless), "local/tts-1")
	if err != nil || got.Secret != "" || got.Model != "tts-1" {
		t.Fatalf("keyless ResolveVoiceTarget() = %#v, %v", got, err)
	}
}

func TestResolveVoiceTargetRejectsUnusableTargets(t *testing.T) {
	disabled := voiceInstance("disabled", "openai")
	disabled.State = config.ProviderInstanceStateDisabled
	cfg := voiceConfig(voiceInstance("voice", "openai"), disabled)
	resolver := stubVoiceCredentials("secret")

	if _, err := resolver.ResolveVoiceTarget(cfg, "  "); !errors.Is(err, ErrNoVoiceTarget) {
		t.Fatalf("blank target error = %v, want ErrNoVoiceTarget", err)
	}
	for _, tc := range []struct{ target, wantErr string }{
		{"whisper", "instance-id/model-id"},
		{"missing/whisper", `"missing" is unavailable`},
		{"disabled/whisper", `"disabled" is unavailable`},
	} {
		if _, err := resolver.ResolveVoiceTarget(cfg, tc.target); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("ResolveVoiceTarget(%q) error = %v, want containing %q", tc.target, err, tc.wantErr)
		}
	}
	if _, err := resolver.ResolveVoiceTarget(nil, "voice/tts"); err == nil {
		t.Fatal("ResolveVoiceTarget(nil config) error = nil")
	}
	failing := NewResolver(WithCredentialResolver(func(string) (string, error) { return "", errors.New("vault sealed") }))
	if _, err := failing.ResolveVoiceTarget(cfg, "voice/tts"); err == nil || !strings.Contains(err.Error(), "vault sealed") {
		t.Fatalf("credential failure error = %v", err)
	}
}

func TestResolveVoiceTargetReadsTheAuthStore(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := auth.SetCredential("voice", &auth.AuthCredential{AccessToken: "voice-secret"}); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveVoiceTarget(voiceConfig(voiceInstance("voice", "openai")), "voice/tts-1")
	if err != nil || got.Secret != "voice-secret" {
		t.Fatalf("ResolveVoiceTarget() = %#v, %v; want the stored credential", got, err)
	}
}
