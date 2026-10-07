package config

import (
	"fmt"
	"strings"
)

// A model selection names the model a setting runs on: an exact target
// "instance-id/model-id" (see ParseExactModelTarget) or the name of a model
// route. The default, image and light models and each agent's model are
// selections.

// ValidateSelectionSyntax checks that s can name a model: empty, an exact
// target, or a model route name. It checks syntax only. Whether the target's
// instance, model or the route exists is checked when a selection is set and
// when it is resolved, so a dangling selection never makes a config
// unloadable.
func ValidateSelectionSyntax(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	if strings.Contains(s, "/") {
		if _, err := ParseExactModelTarget(s); err != nil {
			return fmt.Errorf("selection %q: %w", s, err)
		}
		return nil
	}
	if !providerInstanceIDPattern.MatchString(s) {
		return fmt.Errorf("selection %q must be an exact target instance-id/model-id or a model route name", s)
	}
	return nil
}

// configField is one setting's config path and value.
type configField struct {
	path, value string
}

// ValidateModelSelections checks the syntax of every model selection and of
// the voice targets, which are exact targets. It does not check that they
// exist.
func (c *Config) ValidateModelSelections() error {
	if c == nil {
		return nil
	}
	defaults := c.Agents.Defaults
	selections := []configField{
		{"agents.defaults.model_name", defaults.ModelName},
		{"agents.defaults.image_model", defaults.ImageModel},
	}
	if defaults.Routing != nil {
		selections = append(selections, configField{"agents.defaults.routing.light_model", defaults.Routing.LightModel})
	}
	for i, agent := range c.Agents.List {
		selections = append(selections, configField{fmt.Sprintf("agents.list[%d].model", i), agent.Model})
	}
	for _, selection := range selections {
		if err := ValidateSelectionSyntax(selection.value); err != nil {
			return fmt.Errorf("%s: %w", selection.path, err)
		}
	}

	for _, target := range []configField{
		{"voice.stt_target", c.Voice.STTTarget},
		{"voice.tts_target", c.Voice.TTSTarget},
	} {
		if strings.TrimSpace(target.value) == "" {
			continue
		}
		if _, err := ParseExactModelTarget(target.value); err != nil {
			return fmt.Errorf("%s %q: %w", target.path, target.value, err)
		}
	}
	return nil
}
