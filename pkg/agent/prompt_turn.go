package agent

import (
	"fmt"
	"strings"

	"github.com/xibodev/compa/pkg/config"
	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/providers"
)

func promptBuildRequestForTurn(
	ts *turnState,
	history []providers.Message,
	summary string,
	currentMessage string,
	media []string,
	cfg *config.Config,
) PromptBuildRequest {
	req := PromptBuildRequest{
		History:           history,
		Summary:           summary,
		CurrentMessage:    currentMessage,
		Media:             append([]string(nil), media...),
		Channel:           ts.channel,
		ChatID:            ts.chatID,
		SenderID:          ts.opts.Dispatch.SenderID(),
		SenderDisplayName: ts.opts.SenderDisplayName,
		ActiveSkills:      activeSkillNames(ts.agent, ts.opts),
		Overlays:          append(promptOverlaysForOptions(ts.opts), moduleKnowledgePromptParts(ts.agent, ts.opts)...),
		ModuleSummaries:   ts.agent.ModuleSummaries,
	}
	// The catalog names no tool the approval policy hides from the turn.
	if ts.al != nil && len(req.ModuleSummaries) > 0 {
		if hidden := ts.al.hiddenTools(ts); hidden != nil {
			req.ModuleSummaries = offeredModuleSummaries(req.ModuleSummaries, func(name string) bool {
				tool, _ := ts.agent.Tools.Get(name)
				return hidden(name, tool)
			})
		}
	}
	hasCallableTools := true
	if ts.profile.Enabled {
		defs := ts.agent.Tools.ToProviderDefsForSession(ts.sessionKey)
		if ts.al != nil {
			defs = ts.al.offeredToolDefs(ts)
		}
		hasCallableTools = turnProfileHasCallableTools(ts.profile, defs) ||
			turnProfileNativeSearchCallable(cfg, ts.profile, ts.agent)
	}
	if turnProfileSystemPromptOff(ts.profile) {
		req.SuppressDefaultSystemPrompt = true
		req.SuppressSkillContext = true
		req.ToolUseFallback = hasCallableTools
	}
	if ts.profile.Enabled && !hasCallableTools {
		req.SuppressToolUseRule = true
	}
	if turnProfileSkillsOff(ts.profile) {
		req.SuppressSkillContext = true
	}
	if turnProfileCustomSkills(ts.profile) {
		req.AllowedSkills = append([]string(nil), ts.profile.AllowedSkills...)
	}
	if ts.profile.Enabled && ts.profile.ToolsMode == config.TurnProfileModeCustom {
		req.AllowedTools = append([]string(nil), ts.profile.AllowedTools...)
	}
	return req
}

// moduleCapabilityPrefix starts a capability's line in a module's catalog
// entry, which is a header line and then one "  - <tool> (<capability>): ..."
// line per capability (moduletools' summarize).
const moduleCapabilityPrefix = "  - "

// offeredModuleSummaries returns the catalog entries of summaries without
// the capabilities whose tools hidden reports, leaving out a module none of
// whose capabilities are left. summaries is not modified.
func offeredModuleSummaries(summaries []string, hidden func(name string) bool) []string {
	offered := make([]string, 0, len(summaries))
	for _, summary := range summaries {
		lines := strings.Split(summary, "\n")
		kept := []string{lines[0]}
		capabilities, keptCapabilities := 0, 0
		keep := true // a line that continues a capability's goes with it
		for _, line := range lines[1:] {
			if rest, ok := strings.CutPrefix(line, moduleCapabilityPrefix); ok {
				capabilities++
				name, _, _ := strings.Cut(rest, " (")
				if keep = !hidden(name); keep {
					keptCapabilities++
				}
			}
			if keep {
				kept = append(kept, line)
			}
		}
		if capabilities > 0 && keptCapabilities == 0 {
			continue
		}
		offered = append(offered, strings.Join(kept, "\n"))
	}
	return offered
}

func turnProfileNativeSearchCallable(
	cfg *config.Config,
	profile config.EffectiveTurnProfile,
	agent *AgentInstance,
) bool {
	if cfg == nil || agent == nil {
		return false
	}
	if !cfg.Tools.IsToolEnabled("web") || !cfg.Tools.Web.PreferNative {
		return false
	}
	if !turnProfileToolAllowed(profile, "web_search") {
		return false
	}
	nativeProvider, ok := agent.Provider.(providers.NativeSearchCapable)
	return ok && nativeProvider.SupportsNativeSearch()
}

func promptBuildRequestForProcessOptions(
	agent *AgentInstance,
	opts processOptions,
	history []providers.Message,
	summary string,
	currentMessage string,
	media []string,
) PromptBuildRequest {
	req := PromptBuildRequest{
		History:           history,
		Summary:           summary,
		CurrentMessage:    currentMessage,
		Media:             append([]string(nil), media...),
		Channel:           opts.Dispatch.Channel(),
		ChatID:            opts.Dispatch.ChatID(),
		SenderID:          opts.Dispatch.SenderID(),
		SenderDisplayName: opts.SenderDisplayName,
		ActiveSkills:      activeSkillNames(agent, opts),
		Overlays:          append(promptOverlaysForOptions(opts), moduleKnowledgePromptParts(agent, opts)...),
	}
	profile := opts.TurnProfile
	hasCallableTools := true
	if profile.Enabled && agent != nil {
		hasCallableTools = turnProfileHasCallableTools(profile, agent.Tools.ToProviderDefsForSession(opts.Dispatch.SessionKey))
	}
	if turnProfileSystemPromptOff(profile) {
		req.SuppressDefaultSystemPrompt = true
		req.SuppressSkillContext = true
		req.ToolUseFallback = hasCallableTools
	}
	if profile.Enabled && !hasCallableTools {
		req.SuppressToolUseRule = true
	}
	if turnProfileSkillsOff(profile) {
		req.SuppressSkillContext = true
	}
	if turnProfileCustomSkills(profile) {
		req.AllowedSkills = append([]string(nil), profile.AllowedSkills...)
	}
	if profile.Enabled && profile.ToolsMode == config.TurnProfileModeCustom {
		req.AllowedTools = append([]string(nil), profile.AllowedTools...)
	}
	return req
}

func promptOverlaysForOptions(opts processOptions) []PromptPart {
	systemPrompt := strings.TrimSpace(opts.SystemPromptOverride)
	if systemPrompt == "" {
		return nil
	}

	return []PromptPart{
		{
			ID:      "instruction.subturn_profile",
			Layer:   PromptLayerInstruction,
			Slot:    PromptSlotWorkspace,
			Source:  PromptSource{ID: PromptSourceSubTurnProfile, Name: "subturn.profile"},
			Title:   "SubTurn System Instructions",
			Content: systemPrompt,
			Stable:  false,
			Cache:   PromptCacheNone,
		},
	}
}

func promptContentBlock(part PromptPart, cache *providers.CacheControl) providers.ContentBlock {
	if cache == nil {
		cache = cacheControlForPromptPart(part)
	}
	return providers.ContentBlock{
		Type:         "text",
		Text:         part.Content,
		CacheControl: cache,
		PromptLayer:  string(part.Layer),
		PromptSlot:   string(part.Slot),
		PromptSource: string(part.Source.ID),
	}
}

func cacheControlForPromptPart(part PromptPart) *providers.CacheControl {
	switch part.Cache {
	case PromptCacheEphemeral:
		return &providers.CacheControl{Type: "ephemeral"}
	default:
		return nil
	}
}

func promptMessageWithMetadata(
	msg providers.Message,
	layer PromptLayer,
	slot PromptSlot,
	source PromptSourceID,
) providers.Message {
	msg.PromptLayer = string(layer)
	msg.PromptSlot = string(slot)
	msg.PromptSource = string(source)
	return msg
}

func promptMessageWithDefaultMetadata(
	msg providers.Message,
	layer PromptLayer,
	slot PromptSlot,
	source PromptSourceID,
) providers.Message {
	if strings.TrimSpace(msg.PromptSource) != "" {
		return msg
	}
	return promptMessageWithMetadata(msg, layer, slot, source)
}

func userPromptMessage(content string, media []string) providers.Message {
	msg := providers.Message{
		Role:    "user",
		Content: content,
	}
	if len(media) > 0 {
		msg.Media = append([]string(nil), media...)
	}
	return promptMessageWithMetadata(msg, PromptLayerTurn, PromptSlotMessage, PromptSourceUserMessage)
}

func toolResultPromptMessage(content, toolCallID string, media []string) providers.Message {
	msg := providers.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: toolCallID,
	}
	if len(media) > 0 {
		msg.Media = append([]string(nil), media...)
	}
	return promptMessageWithMetadata(msg, PromptLayerTurn, PromptSlotToolResult, PromptSourceToolResult)
}

func toolImageFollowUpPromptMessage(media []string) providers.Message {
	msg := providers.Message{
		Role:    "user",
		Content: "[Loaded image from tool result above]",
	}
	if len(media) > 0 {
		msg.Media = append([]string(nil), media...)
	}
	return promptMessageWithMetadata(msg, PromptLayerTurn, PromptSlotToolResult, PromptSourceToolResult)
}

func steeringPromptMessage(msg providers.Message) providers.Message {
	return promptMessageWithDefaultMetadata(msg, PromptLayerTurn, PromptSlotSteering, PromptSourceSteering)
}

func subTurnResultPromptMessage(content string) providers.Message {
	return promptMessageWithMetadata(
		providers.Message{Role: "user", Content: fmt.Sprintf("[SubTurn Result] %s", content)},
		PromptLayerTurn,
		PromptSlotSubTurn,
		PromptSourceSubTurnResult,
	)
}

// takeSubTurnResult returns the prompt message of a sub-turn result that is
// waiting for ts, without blocking. The result is filtered by the turn's
// config.
func (p *Pipeline) takeSubTurnResult(ts *turnState) (providers.Message, bool) {
	if ts == nil || ts.pendingResults == nil {
		return providers.Message{}, false
	}
	select {
	case result, ok := <-ts.pendingResults:
		if ok && result != nil && result.ForLLM != "" {
			return subTurnResultPromptMessage(p.Cfg.FilterSensitiveData(result.ForLLM)), true
		}
	default:
	}
	return providers.Message{}, false
}

// moduleKnowledgePromptParts loads a selected module's overlay and skills.
//
// This is the deeper half of the connector gesture. Every installed module
// contributes one line of capability summary always, so the agent knows what
// exists; a module's own instructions and skills cost real context and load
// only when a user points the agent at it.
//
// Nothing is loaded without a selection, and nothing is loaded for a module
// that is not installed -- the loader matches against the descriptors
// discovered at startup, so a selection naming something absent contributes
// nothing rather than erroring the turn.
//
// Content that fails its declared digest is REFUSED by the loader and reported
// as a warning rather than loaded: a module's documentation is untrusted input
// like anything else it produces, and a silently missing overlay would mean the
// agent behaves differently with no visible reason.
func moduleKnowledgePromptParts(agent *AgentInstance, opts processOptions) []PromptPart {
	if agent == nil || agent.ModuleKnowledge == nil {
		return nil
	}
	moduleID := opts.Dispatch.SelectedModule()
	if moduleID == "" {
		return nil
	}

	overlays, skills, warnings := agent.ModuleKnowledge(moduleID)
	for _, w := range warnings {
		logger.WarnCF("modules", "module knowledge was declared but could not be used",
			map[string]any{"module": moduleID, "warning": w})
	}

	// A selection the host cannot honour is CORRECTED in the prompt, not just
	// logged.
	//
	// The channel states the selection to the model as an instruction the user
	// can see in their transcript, and it does that before anything knows
	// whether the module exists -- a stale client, a removed module or a typo
	// all produce "[Use the ghostmodule module for this request...]". Telling a
	// model to prefer the capabilities of something that does not exist is an
	// instruction it can only follow by improvising, which is precisely the
	// behaviour this boundary exists to prevent.
	//
	// A log line does not reach the model. This does, in the same channel the
	// wrong instruction arrived on.
	if len(warnings) > 0 && strings.TrimSpace(overlays) == "" && strings.TrimSpace(skills) == "" {
		return []PromptPart{{
			ID:     "instruction.module_unavailable",
			Layer:  PromptLayerInstruction,
			Slot:   PromptSlotWorkspace,
			Source: PromptSource{ID: PromptSourceModuleKnowledge, Name: "module:" + moduleID},
			Title:  "module unavailable: " + moduleID,
			// The warning carries WHY -- not installed, or installed but
			// disabled -- and the two need different actions from the user.
			// Saying "not installed" about a module they can see on the Modules
			// page would send them to reinstall something they already have.
			Content: fmt.Sprintf("The module %q named in this turn's instruction"+
				" is unavailable: %s.\n\nIt contributes no capabilities and no"+
				" guidance. Ignore the instruction to prefer it. Use the tools"+
				" you actually have, and if the user's request depends on that"+
				" module, say plainly that it is unavailable AND why, rather"+
				" than approximating its work with general-purpose tools or the"+
				" shell.", moduleID, strings.Join(warnings, "; ")),
			Stable: false,
			Cache:  PromptCacheNone,
		}}
	}

	var parts []PromptPart
	if strings.TrimSpace(overlays) != "" {
		parts = append(parts, PromptPart{
			ID:      "instruction.module_overlay",
			Layer:   PromptLayerInstruction,
			Slot:    PromptSlotWorkspace,
			Source:  PromptSource{ID: PromptSourceModuleKnowledge, Name: "module:" + moduleID},
			Title:   "module instructions: " + moduleID,
			Content: overlays,
			Stable:  false,
			Cache:   PromptCacheNone,
		})
	}
	if strings.TrimSpace(skills) != "" {
		parts = append(parts, PromptPart{
			ID:      "capability.module_skills",
			Layer:   PromptLayerCapability,
			Slot:    PromptSlotActiveSkill,
			Source:  PromptSource{ID: PromptSourceModuleKnowledge, Name: "module:" + moduleID},
			Title:   "module skills: " + moduleID,
			Content: skills,
			Stable:  false,
			Cache:   PromptCacheNone,
		})
	}
	if len(parts) > 0 {
		logger.InfoCF("modules", "module knowledge composed for this turn", map[string]any{
			"module": moduleID, "parts": len(parts),
		})
	}
	return parts
}
