package modelservice

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xibodev/compa/pkg/config"
)

// ErrNoVoiceTarget reports a voice request whose target is not configured.
var ErrNoVoiceTarget = errors.New("voice target is not configured")

// ErrNoAudioInput reports a chat model voice would transcribe with whose
// catalog does not declare that it takes audio.
var ErrNoAudioInput = errors.New("does not accept audio input")

// CheckChatTranscriptionTarget reports whether target, the chat model voice
// transcribes with (config.VoiceConfig.STTViaChat), takes audio: the saved
// catalog of its instance must declare audio input for its model (see
// CatalogModel.AudioInput). A chat model without audio input answers a
// recording it never received with text of its own, which would pass for
// what the user said. A nil store reads the saved catalogs.
func CheckChatTranscriptionTarget(cfg *config.Config, store *CatalogStore, target string) error {
	if cfg == nil {
		return errors.New("config is required")
	}
	parsed, err := config.ParseExactModelTarget(strings.TrimSpace(target))
	if err != nil {
		return err
	}
	if store == nil {
		if store, err = LoadCatalogs(); err != nil {
			return err
		}
	}
	for _, instance := range cfg.ProviderInstances {
		if instance == nil || instance.ID != parsed.InstanceID {
			continue
		}
		entry := store.Entries[instance.ID]
		if !ValidInstanceCatalog(instance.ID, entry, instance) {
			break
		}
		for _, model := range entry.Models {
			if strings.TrimSpace(model.ID) == parsed.ModelID && model.AudioInput {
				return nil
			}
		}
	}
	return fmt.Errorf("model %q %w; choose a speech-to-text model, or a chat model whose catalog declares audio input", parsed.ModelID, ErrNoAudioInput)
}

// VoiceTarget is what an exact voice target "instance-id/model-id" names:
// an enabled provider instance, one of its models and the instance's secret.
type VoiceTarget struct {
	Instance *config.ProviderInstanceConfig
	Model    string
	// Secret is what the instance's auth_connection_ref resolved to; empty
	// when it has none.
	Secret string
}

// ResolveVoiceTarget resolves target to its enabled provider instance, model
// and secret, read from the auth store.
func ResolveVoiceTarget(cfg *config.Config, target string) (VoiceTarget, error) {
	return NewResolver().ResolveVoiceTarget(cfg, target)
}

// ResolveVoiceTarget is ResolveVoiceTarget resolving the instance's secret
// with r's credential resolver.
func (r *Resolver) ResolveVoiceTarget(cfg *config.Config, target string) (VoiceTarget, error) {
	if cfg == nil {
		return VoiceTarget{}, errors.New("config is required")
	}
	if strings.TrimSpace(target) == "" {
		return VoiceTarget{}, ErrNoVoiceTarget
	}
	parsed, err := config.ParseExactModelTarget(strings.TrimSpace(target))
	if err != nil {
		return VoiceTarget{}, err
	}
	var instance *config.ProviderInstanceConfig
	for _, candidate := range cfg.ProviderInstances {
		if candidate != nil && candidate.ID == parsed.InstanceID {
			instance = candidate
			break
		}
	}
	if instance == nil || instance.State != config.ProviderInstanceStateEnabled {
		return VoiceTarget{}, fmt.Errorf("voice target provider %q is unavailable", parsed.InstanceID)
	}
	secret := ""
	if ref := strings.TrimSpace(instance.AuthConnectionRef); ref != "" {
		if secret, err = r.resolveCredential(ref); err != nil {
			return VoiceTarget{}, err
		}
	}
	return VoiceTarget{Instance: instance, Model: parsed.ModelID, Secret: secret}, nil
}

func cloneHeaders(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	for name, value := range source {
		clone[name] = value
	}
	return clone
}
