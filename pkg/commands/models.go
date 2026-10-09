package commands

import (
	"fmt"
	"strings"

	"github.com/xibodev/compa/v4/pkg/config"
)

// noModelSelectedMsg is the reply when the agent has no model.
const noModelSelectedMsg = "No model selected. Connect a provider under Models and choose a default model."

// selectionUsage names what /switch model accepts.
const selectionUsage = "<instance-id/model-id or route>"

// formatModelInfo renders /show model: the agent's selection and the
// target serving it, or why it has none.
func formatModelInfo(info ModelInfo) string {
	selection := strings.TrimSpace(info.Selection)
	if selection == "" {
		return noModelSelectedMsg
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Current Model: %s", selection)
	switch served := strings.TrimSpace(info.ServedTarget); {
	case strings.TrimSpace(info.Unavailable) != "":
		fmt.Fprintf(&b, "\nUnavailable: %s", strings.TrimSpace(info.Unavailable))
	case served != "" && served != selection:
		fmt.Fprintf(&b, "\nServed by: %s", served)
	}
	return b.String()
}

// formatModelList renders /list models: the shortlist of models offered for
// chat, the model routes, the default model and the agent's own.
func formatModelList(cfg *config.Config, current ModelInfo) string {
	var active []string
	var routes []*config.ModelRouteConfig
	var defaultSelection string
	if cfg != nil {
		active = cfg.ActiveModels
		routes = cfg.ModelRoutes
		defaultSelection = strings.TrimSpace(cfg.Agents.Defaults.GetModelName())
	}
	currentSelection := strings.TrimSpace(current.Selection)
	if len(active) == 0 && len(routes) == 0 && defaultSelection == "" && currentSelection == "" {
		return "No models are set up. Connect a provider under Models and choose a default model."
	}

	mark := func(selection string) string {
		var tags []string
		if selection == defaultSelection {
			tags = append(tags, "default")
		}
		if selection == currentSelection {
			tags = append(tags, "current")
		}
		if len(tags) == 0 {
			return selection
		}
		return fmt.Sprintf("%s (%s)", selection, strings.Join(tags, ", "))
	}

	var b strings.Builder
	b.WriteString("Models:")
	if len(active) == 0 {
		b.WriteString("\n(none shortlisted)")
	}
	for _, target := range active {
		if target = strings.TrimSpace(target); target != "" {
			fmt.Fprintf(&b, "\n- %s", mark(target))
		}
	}
	if len(routes) > 0 {
		b.WriteString("\n\nRoutes:")
		for _, route := range routes {
			if route == nil {
				continue
			}
			fmt.Fprintf(&b, "\n- %s: %s", mark(route.Name), strings.Join(route.Targets, " -> "))
		}
	}

	b.WriteString("\n\nDefault: ")
	if defaultSelection == "" {
		b.WriteString("none")
	} else {
		b.WriteString(defaultSelection)
	}
	if currentSelection != "" && currentSelection != defaultSelection {
		fmt.Fprintf(&b, "\nCurrent: %s", currentSelection)
	}
	fmt.Fprintf(&b, "\n\nUse /switch model to %s to change this agent's model.", selectionUsage)
	return b.String()
}
