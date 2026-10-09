package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/providers"
	"github.com/xibodev/compa/v4/pkg/skills"
	"github.com/xibodev/compa/v4/pkg/tokenizer"
	"github.com/xibodev/compa/v4/pkg/utils"
)

type ContextBuilder struct {
	workspace      string
	skillsLoader   *skills.SkillsLoader
	memory         *MemoryStore
	splitOnMarker  bool
	agentDiscovery func(agentID string) []AgentDescriptor
	promptRegistry *PromptRegistry
	// spokenReplies is whether the user can turn on spoken replies. Only an
	// identity that AGENT.md names depends on it.
	spokenReplies bool

	// Cache for system prompt to avoid rebuilding on every call.
	// This fixes issue #607: repeated reprocessing of the entire context.
	// The cache auto-invalidates when workspace source files change (mtime check).
	systemPromptMutex  sync.RWMutex
	cachedSystemPrompt string
	cachedAt           time.Time // max observed mtime across tracked paths at cache build time

	// existedAtCache tracks which source file paths existed the last time the
	// cache was built. This lets sourceFilesChanged detect files that are newly
	// created (didn't exist at cache time, now exist) or deleted (existed at
	// cache time, now gone) — both of which should trigger a cache rebuild.
	existedAtCache map[string]bool

	// skillFilesAtCache snapshots the skill tree file set and mtimes at cache
	// build time. This catches nested file creations/deletions/mtime changes
	// that may not update the top-level skill root directory mtime.
	skillFilesAtCache map[string]time.Time

	// cachedDay is the local date (YYYYMMDD) the cache was built on. The
	// prompt carries the recent daily notes, which move with the date.
	cachedDay string
}

func (cb *ContextBuilder) WithToolDiscovery(useBM25, useRegex bool) *ContextBuilder {
	if useBM25 || useRegex {
		if err := cb.RegisterPromptContributor(toolDiscoveryPromptContributor{
			useBM25:  useBM25,
			useRegex: useRegex,
		}); err != nil {
			logger.WarnCF(
				"agent",
				"Failed to register tool discovery prompt contributor",
				map[string]any{
					"error": err.Error(),
				},
			)
		}
	}
	return cb
}

func (cb *ContextBuilder) WithSplitOnMarker(enabled bool) *ContextBuilder {
	cb.splitOnMarker = enabled
	return cb
}

// withSpokenReplies records whether the user can turn on spoken replies.
func (cb *ContextBuilder) withSpokenReplies(enabled bool) *ContextBuilder {
	cb.spokenReplies = enabled
	cb.InvalidateCache()
	return cb
}

func (cb *ContextBuilder) WithAgentDiscovery(
	agentID string,
	discover func(agentID string) []AgentDescriptor,
) *ContextBuilder {
	cb.agentDiscovery = discover
	if discover != nil {
		if err := cb.RegisterPromptContributor(agentDiscoveryPromptContributor{
			agentID:  agentID,
			discover: discover,
		}); err != nil {
			logger.WarnCF(
				"agent",
				"Failed to register agent discovery prompt contributor",
				map[string]any{
					"error": err.Error(),
				},
			)
		}
	}
	return cb
}

func getGlobalConfigDir() string {
	return config.GetHome()
}

// globalSkillsDir is the folder of the skills every workspace's agent sees,
// $COMPA_HOME/skills.
func globalSkillsDir() string {
	return filepath.Join(getGlobalConfigDir(), "skills")
}

func NewContextBuilder(workspace string) *ContextBuilder {
	// Built-in skills come from $COMPA_BUILTIN_SKILLS or the install, never
	// from the working directory: a skills/ folder where Compa happens to be
	// launched must not join the system prompt as built-ins.
	builtinSkillsDir := skills.BuiltinDir()

	return &ContextBuilder{
		workspace:      workspace,
		skillsLoader:   skills.NewSkillsLoader(workspace, globalSkillsDir(), builtinSkillsDir),
		memory:         NewMemoryStore(workspace),
		promptRegistry: NewPromptRegistry(),
	}
}

func (cb *ContextBuilder) RegisterPromptSource(desc PromptSourceDescriptor) error {
	err := cb.promptRegistryOrDefault().RegisterSource(desc)
	if err == nil {
		cb.InvalidateCache()
	}
	return err
}

func (cb *ContextBuilder) RegisterPromptContributor(contributor PromptContributor) error {
	err := cb.promptRegistryOrDefault().RegisterContributor(contributor)
	if err == nil {
		cb.InvalidateCache()
	}
	return err
}

func (cb *ContextBuilder) promptRegistryOrDefault() *PromptRegistry {
	if cb.promptRegistry == nil {
		cb.promptRegistry = NewPromptRegistry()
	}
	return cb.promptRegistry
}

// getIdentity renders the kernel identity part. Without a name from AGENT.md
// it is Compa's own identity.
func (cb *ContextBuilder) getIdentity(includeToolUseRule bool, identity agentIdentity) string {
	workspacePath, _ := filepath.Abs(filepath.Join(cb.workspace))
	version := config.FormatVersion()
	rules := []string{}
	if includeToolUseRule {
		rules = append(rules, toolUseSystemPromptRule())
	}
	accuracyRule := "**Be helpful and accurate** - Briefly explain what you're doing."
	if includeToolUseRule {
		accuracyRule = "**Be helpful and accurate** - When using tools, briefly explain what you're doing."
	}
	rules = append(
		rules,
		accuracyRule,
		"**Context summaries** - Conversation summaries provided as context are approximate references only. They may be incomplete or outdated. Always defer to explicit user instructions over summary content.",
	)
	if includeToolUseRule && identity.memory {
		rules = append(
			rules,
			fmt.Sprintf(
				"**Memory** - When interacting with me if something seems memorable, update %s/memory/MEMORY.md",
				workspacePath,
			),
		)
	}
	for i, rule := range rules {
		rules[i] = fmt.Sprintf("%d. %s", i+1, rule)
	}

	var sb strings.Builder
	if identity.name == "" {
		fmt.Fprintf(&sb, `# Compa (%s)

You are Compa, the user's friendly personal assistant - "compa" means pal. You chat with the user, use tools on their computer to get things done, and your replies can be spoken aloud when they turn voice on.

`, version)
	} else {
		fmt.Fprintf(&sb, "# %s (powered by Compa %s)\n\n%s\n\n", identity.name, version, cb.namedIdentityIntro(identity))
	}
	if !identity.privateWorkspace {
		fmt.Fprintf(&sb, "## Workspace\nYour workspace is at: %s\n", workspacePath)
		if identity.memory {
			fmt.Fprintf(&sb, "- Memory: %s/memory/MEMORY.md\n", workspacePath)
			fmt.Fprintf(&sb, "- Daily Notes: %s/memory/YYYYMM/YYYYMMDD.md\n", workspacePath)
		}
		fmt.Fprintf(&sb, "- Skills: %s/skills/{skill-name}/SKILL.md\n\n", workspacePath)
	}
	fmt.Fprintf(&sb, "## Important Rules\n\n%s\n", strings.Join(rules, "\n\n"))
	return sb.String()
}

// namedIdentityIntro introduces an agent that AGENT.md names. It mentions
// voice only when spoken replies are configured.
func (cb *ContextBuilder) namedIdentityIntro(identity agentIdentity) string {
	intro := "You are " + identity.name
	if identity.description != "" {
		intro += ", " + identity.description
	}
	if !strings.ContainsAny(intro[len(intro)-1:], ".!?") {
		intro += "."
	}
	intro += " Compa is the agent runtime that powers you."
	if cb.spokenReplies {
		intro += " Your replies can be spoken aloud when the user turns voice on."
	}
	return intro
}

func formatToolDiscoveryRule(useBM25, useRegex bool) string {
	if !useBM25 && !useRegex {
		return ""
	}

	var toolNames []string
	if useBM25 {
		toolNames = append(toolNames, `"tool_search_tool_bm25"`)
	}
	if useRegex {
		toolNames = append(toolNames, `"tool_search_tool_regex"`)
	}

	return fmt.Sprintf(
		`5. **Tool Discovery** - Your visible tools are limited to save memory, but a vast hidden library exists. If you lack the right tool for a task, BEFORE giving up, you MUST search using the %s tool. Do not refuse a request unless the search returns nothing. Found tools will temporarily unlock for your next turn.`,
		strings.Join(toolNames, " or "),
	)
}

func (cb *ContextBuilder) BuildSystemPrompt() string {
	return renderPromptParts(cb.BuildSystemPromptParts())
}

// moduleCatalogPart is the prompt part that lists the enabled module
// capabilities, one line each; false when there are none.
//
// One line per capability, deliberately: an enabled module should cost
// prompt budget proportional to what it offers, and eagerly loading every
// module's documentation is exactly the failure layered composition exists
// to prevent. Overlays and skills load later, and only when selected.
func moduleCatalogPart(summaries []string) (PromptPart, bool) {
	if len(summaries) == 0 {
		return PromptPart{}, false
	}
	return PromptPart{
		ID:     "capability.module_catalog",
		Layer:  PromptLayerCapability,
		Slot:   PromptSlotTooling,
		Source: PromptSource{ID: PromptSourceToolRegistry, Name: "module:index"},
		Title:  "installed modules",
		Content: fmt.Sprintf(`# Installed modules

These capabilities come from detached modules installed in this host. Call them
by their tool name. A capability marked "cost unknown" may bill real money, so
prefer a local or free capability when one will do.

%s`, strings.Join(summaries, "\n\n")),
		Stable: true,
		Cache:  PromptCacheEphemeral,
	}, true
}

func (cb *ContextBuilder) BuildSystemPromptParts() []PromptPart {
	return cb.buildSystemPromptParts(systemPromptBuildOptions{
		IncludeSkillCatalog: true,
		IncludeToolUseRule:  true,
	})
}

type systemPromptBuildOptions struct {
	IncludeSkillCatalog bool
	// ModuleSummaries are one-line capability summaries for each enabled
	// detached module, composed so the agent knows what its modules offer
	// without loading their full documentation.
	ModuleSummaries    []string
	IncludeToolUseRule bool
	AllowedSkills      []string
	AllowedTools       []string
}

func (cb *ContextBuilder) buildSystemPromptParts(opts systemPromptBuildOptions) []PromptPart {
	stack := NewPromptStack(cb.promptRegistryOrDefault())
	add := func(part PromptPart) {
		if err := stack.Add(part); err != nil {
			logger.WarnCF("agent", "Skipping invalid prompt part", map[string]any{
				"id":     part.ID,
				"layer":  part.Layer,
				"slot":   part.Slot,
				"source": part.Source.ID,
				"error":  err.Error(),
			})
		}
	}

	// One read of the definition files per build: AGENT.md frontmatter
	// shapes the identity, and the files themselves are the bootstrap.
	definition := cb.LoadAgentDefinition()
	identity := definition.identity()

	// Core identity section
	add(PromptPart{
		ID:      "kernel.identity",
		Layer:   PromptLayerKernel,
		Slot:    PromptSlotIdentity,
		Source:  PromptSource{ID: PromptSourceKernel, Name: "identity"},
		Title:   "Compa identity",
		Content: cb.getIdentity(opts.IncludeToolUseRule, identity),
		Stable:  true,
		Cache:   PromptCacheEphemeral,
	})

	// Bootstrap files
	bootstrapContent := cb.bootstrapContent(definition)
	if bootstrapContent != "" {
		add(PromptPart{
			ID:      "instruction.workspace",
			Layer:   PromptLayerInstruction,
			Slot:    PromptSlotWorkspace,
			Source:  PromptSource{ID: PromptSourceWorkspace, Name: "workspace"},
			Title:   "workspace instructions",
			Content: bootstrapContent,
			Stable:  true,
			Cache:   PromptCacheEphemeral,
		})
	}

	// Skills - show summary, AI can read full content with read_file tool
	// Enabled module capabilities.
	if part, ok := moduleCatalogPart(opts.ModuleSummaries); ok {
		add(part)
	}

	skillsSummary := ""
	if opts.IncludeSkillCatalog {
		skillsSummary = cb.buildSkillsSummary(opts.AllowedSkills)
	}
	if skillsSummary != "" {
		skillIntro := "The following skills extend your capabilities."
		readFileAllowed := promptAllowsTool(
			PromptBuildRequest{AllowedTools: opts.AllowedTools},
			"read_file",
		)
		if opts.IncludeToolUseRule && readFileAllowed {
			skillIntro += " To use a skill, read its SKILL.md file using the read_file tool."
		}
		add(PromptPart{
			ID:     "capability.skill_catalog",
			Layer:  PromptLayerCapability,
			Slot:   PromptSlotSkillCatalog,
			Source: PromptSource{ID: PromptSourceSkillCatalog, Name: "skill:index"},
			Title:  "skill catalog",
			Content: fmt.Sprintf(`# Skills

%s

%s`, skillIntro, skillsSummary),
			Stable: true,
			Cache:  PromptCacheEphemeral,
		})
	}

	// Memory context, unless AGENT.md turns memory off
	memoryContext := ""
	if identity.memory {
		memoryContext = cb.memory.GetMemoryContext()
	}
	if memoryContext != "" {
		add(PromptPart{
			ID:      "context.memory",
			Layer:   PromptLayerContext,
			Slot:    PromptSlotMemory,
			Source:  PromptSource{ID: PromptSourceMemory, Name: "memory:workspace"},
			Title:   "memory",
			Content: "# Memory\n\n" + memoryContext,
			Stable:  true,
			Cache:   PromptCacheEphemeral,
		})
	}

	// Multi-Message Sending (if enabled)
	if cb.splitOnMarker {
		add(PromptPart{
			ID:     "context.output_policy.split_on_marker",
			Layer:  PromptLayerContext,
			Slot:   PromptSlotOutput,
			Source: PromptSource{ID: PromptSourceOutputPolicy, Name: "split_on_marker"},
			Title:  "multi-message output policy",
			Content: `# MULTI-MESSAGE OUTPUT
You MUST frequently use <|[SPLIT]|> to break your responses into multiple short messages. NEVER output a single long wall of text. Actively split distinct concepts or parts. Example: Message part 1<|[SPLIT]|>Message part 2<|[SPLIT]|>Message part 3

Each part separated by the marker will be sent as an independent message.`,
			Stable: true,
			Cache:  PromptCacheEphemeral,
		})
	}

	stack.Seal()
	return stack.Parts()
}

// BuildSystemPromptWithCache returns the cached system prompt if available
// and source files haven't changed, otherwise builds and caches it.
// Source file changes are detected via mtime checks (cheap stat calls).
func (cb *ContextBuilder) BuildSystemPromptWithCache() string {
	// Try read lock first — fast path when cache is valid
	cb.systemPromptMutex.RLock()
	if cb.cachedSystemPrompt != "" && !cb.sourceFilesChangedLocked() {
		result := cb.cachedSystemPrompt
		cb.systemPromptMutex.RUnlock()
		return result
	}
	cb.systemPromptMutex.RUnlock()

	// Acquire write lock for building
	cb.systemPromptMutex.Lock()
	defer cb.systemPromptMutex.Unlock()

	// Double-check: another goroutine may have rebuilt while we waited
	if cb.cachedSystemPrompt != "" && !cb.sourceFilesChangedLocked() {
		return cb.cachedSystemPrompt
	}

	// Snapshot the baseline (existence + max mtime) BEFORE building the prompt.
	// This way cachedAt reflects the pre-build state: if a file is modified
	// during BuildSystemPrompt, its new mtime will be > baseline.maxMtime,
	// so the next sourceFilesChangedLocked check will correctly trigger a
	// rebuild. The alternative (baseline after build) risks caching stale
	// content with a too-new baseline, making the staleness invisible.
	baseline := cb.buildCacheBaseline()
	prompt := cb.BuildSystemPrompt()
	cb.cachedSystemPrompt = prompt
	cb.cachedAt = baseline.maxMtime
	cb.existedAtCache = baseline.existed
	cb.skillFilesAtCache = baseline.skillFiles
	cb.cachedDay = baseline.day

	logger.DebugCF("agent", "System prompt cached",
		map[string]any{
			"length": len(prompt),
		})

	return prompt
}

func (cb *ContextBuilder) buildSystemPromptForRequest(
	req PromptBuildRequest,
) (string, []providers.ContentBlock) {
	if req.SuppressDefaultSystemPrompt {
		return "", nil
	}

	useDefaultCache := !req.SuppressSkillContext &&
		!req.SuppressToolUseRule &&
		len(req.AllowedSkills) == 0 &&
		len(req.AllowedTools) == 0
	if useDefaultCache {
		staticPrompt := cb.BuildSystemPromptWithCache()
		blocks := []providers.ContentBlock{
			promptContentBlock(PromptPart{
				ID:      "kernel.static",
				Layer:   PromptLayerKernel,
				Slot:    PromptSlotIdentity,
				Source:  PromptSource{ID: PromptSourceKernel, Name: "static"},
				Content: staticPrompt,
			}, &providers.CacheControl{Type: "ephemeral"}),
		}
		// The cached prompt is the agent's; the module catalog is the turn's
		// (the approval policy may hide tools from it), so it follows the
		// cached block, without a cache breakpoint of its own.
		if part, ok := moduleCatalogPart(req.ModuleSummaries); ok {
			part.Cache = PromptCacheNone
			staticPrompt = renderPromptParts([]PromptPart{
				{ID: "kernel.static", Layer: PromptLayerKernel, Slot: PromptSlotIdentity, Content: staticPrompt},
				part,
			})
			blocks = append(blocks, promptContentBlock(part, nil))
		}
		return staticPrompt, blocks
	}

	parts := cb.buildSystemPromptParts(systemPromptBuildOptions{
		IncludeSkillCatalog: !req.SuppressSkillContext,
		IncludeToolUseRule:  !req.SuppressToolUseRule,
		AllowedSkills:       req.AllowedSkills,
		AllowedTools:        req.AllowedTools,
		ModuleSummaries:     req.ModuleSummaries,
	})
	staticPrompt := renderPromptParts(parts)
	blocks := make([]providers.ContentBlock, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part.Content) == "" {
			continue
		}
		blocks = append(blocks, promptContentBlock(part, cacheControlForPromptPart(part)))
	}
	return staticPrompt, blocks
}

func (cb *ContextBuilder) buildSkillsSummary(allowed []string) string {
	if cb.skillsLoader == nil {
		return ""
	}
	if len(allowed) == 0 {
		return cb.skillsLoader.BuildSkillsSummary()
	}
	allowedSet := cleanAllowedSet(allowed)
	if len(allowedSet) == 0 {
		return ""
	}

	var lines []string
	lines = append(lines, "<skills>")
	for _, s := range cb.skillsLoader.ListSkills() {
		if _, ok := allowedSet[strings.ToLower(strings.TrimSpace(s.Name))]; !ok {
			continue
		}
		lines = append(lines, "  <skill>")
		lines = append(lines, fmt.Sprintf("    <name>%s</name>", xmlEscapeForPrompt(s.Name)))
		lines = append(
			lines,
			fmt.Sprintf("    <description>%s</description>", xmlEscapeForPrompt(s.Description)),
		)
		lines = append(
			lines,
			fmt.Sprintf("    <location>%s</location>", xmlEscapeForPrompt(s.Path)),
		)
		lines = append(lines, fmt.Sprintf("    <source>%s</source>", xmlEscapeForPrompt(s.Source)))
		lines = append(lines, "  </skill>")
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(lines, "</skills>")
	return strings.Join(lines, "\n")
}

func xmlEscapeForPrompt(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

// EstimateSystemTokens estimates the token count of the full system message
// that would be sent to the LLM, mirroring the composition logic in BuildMessagesFromPrompt.
// It includes: static prompt, dynamic context, active skills, and summary with
// wrapping prefixes and separators. This avoids needing all per-request parameters
// that BuildMessagesFromPrompt requires (media, channel, chatID, sender, etc.).
func (cb *ContextBuilder) EstimateSystemTokens(summary string, activeSkills []string) int {
	staticPrompt := cb.BuildSystemPromptWithCache()

	// Dynamic context is small and varies per request; use a representative estimate.
	// Actual buildDynamicContext produces ~200-400 chars of time/runtime/session info.
	const dynamicContextChars = 300

	// The text is estimated as tokenizer.EstimateMessageTokens does (CJK
	// aware); the fixed overheads are ASCII.
	var text strings.Builder
	text.WriteString(staticPrompt)
	overheadChars := dynamicContextChars

	if skillsText := cb.buildActiveSkillsContext(activeSkills); skillsText != "" {
		text.WriteString(skillsText)
		overheadChars += 7 // separator \n\n---\n\n
	}

	if contributedParts, err := cb.promptRegistryOrDefault().Collect(context.Background(), PromptBuildRequest{
		Summary:      summary,
		ActiveSkills: append([]string(nil), activeSkills...),
	}); err == nil {
		for _, part := range contributedParts {
			if strings.TrimSpace(part.Content) == "" {
				continue
			}
			text.WriteString(part.Content)
			overheadChars += 7 // separator
		}
	}

	if summary != "" {
		// Matches the CONTEXT_SUMMARY: prefix added in BuildMessagesFromPrompt
		const summaryPrefix = "CONTEXT_SUMMARY: The following is an approximate summary of prior conversation " +
			"for reference only. It may be incomplete or outdated — always defer to explicit instructions.\n\n"
		text.WriteString(summaryPrefix)
		text.WriteString(summary)
		overheadChars += 7 // separator
	}

	return tokenizer.EstimateTextTokens(text.String()) + overheadChars*2/5
}

// InvalidateCache clears the cached system prompt.
// Normally not needed because the cache auto-invalidates via mtime checks,
// but this is useful for tests or explicit reload commands.
func (cb *ContextBuilder) InvalidateCache() {
	cb.systemPromptMutex.Lock()
	defer cb.systemPromptMutex.Unlock()

	cb.cachedSystemPrompt = ""
	cb.cachedAt = time.Time{}
	cb.existedAtCache = nil
	cb.skillFilesAtCache = nil
	cb.cachedDay = ""

	logger.DebugCF("agent", "System prompt cache invalidated", nil)
}

// sourcePaths returns non-skill workspace source files tracked for cache
// invalidation (bootstrap files, memory and the daily notes the prompt
// carries today). Skill roots are handled separately because they require
// both directory-level and recursive file-level checks.
func (cb *ContextBuilder) sourcePaths() []string {
	paths := agentDefinitionPaths(cb.workspace)
	memoryDir := filepath.Join(cb.workspace, "memory")
	paths = append(paths, filepath.Join(memoryDir, "MEMORY.md"))
	paths = append(paths, dailyNotePaths(memoryDir, time.Now(), recentDailyNoteDays)...)
	return uniquePaths(paths)
}

// skillRoots returns all skill root directories that can affect
// BuildSkillsSummary output (workspace/global/builtin).
func (cb *ContextBuilder) skillRoots() []string {
	if cb.skillsLoader == nil {
		return []string{filepath.Join(cb.workspace, "skills")}
	}

	roots := cb.skillsLoader.SkillRoots()
	if len(roots) == 0 {
		return []string{filepath.Join(cb.workspace, "skills")}
	}
	return roots
}

// cacheBaseline holds the file existence snapshot and the latest observed
// mtime across all tracked paths. Used as the cache reference point.
type cacheBaseline struct {
	existed    map[string]bool
	skillFiles map[string]time.Time
	maxMtime   time.Time
	day        string
}

// buildCacheBaseline records which tracked paths currently exist and computes
// the latest mtime across all tracked files + skills directory contents.
// Called under write lock when the cache is built.
func (cb *ContextBuilder) buildCacheBaseline() cacheBaseline {
	day := time.Now().Format("20060102")
	skillRoots := cb.skillRoots()

	// All paths whose existence we track: source files + all skill roots.
	allPaths := append(cb.sourcePaths(), skillRoots...)

	existed := make(map[string]bool, len(allPaths))
	skillFiles := make(map[string]time.Time)
	var maxMtime time.Time

	for _, p := range allPaths {
		info, err := os.Stat(p)
		existed[p] = err == nil
		if err == nil && info.ModTime().After(maxMtime) {
			maxMtime = info.ModTime()
		}
	}

	// Walk all skill roots recursively to snapshot skill files and mtimes.
	// Use os.Stat (not d.Info) for consistency with sourceFilesChanged checks.
	for _, root := range skillRoots {
		_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr == nil && !d.IsDir() {
				if info, err := os.Stat(path); err == nil {
					skillFiles[path] = info.ModTime()
					if info.ModTime().After(maxMtime) {
						maxMtime = info.ModTime()
					}
				}
			}
			return nil
		})
	}

	// If no tracked files exist yet (empty workspace), maxMtime is zero.
	// Use a very old non-zero time so that:
	// 1. cachedAt.IsZero() won't trigger perpetual rebuilds.
	// 2. Any real file created afterwards has mtime > cachedAt, so it
	//    will be detected by fileChangedSince (unlike time.Now() which
	//    could race with a file whose mtime <= Now).
	if maxMtime.IsZero() {
		maxMtime = time.Unix(1, 0)
	}

	return cacheBaseline{existed: existed, skillFiles: skillFiles, maxMtime: maxMtime, day: day}
}

// sourceFilesChangedLocked checks whether any workspace source file has been
// modified, created, or deleted since the cache was last built.
//
// IMPORTANT: The caller MUST hold at least a read lock on systemPromptMutex.
// Go's sync.RWMutex is not reentrant, so this function must NOT acquire the
// lock itself (it would deadlock when called from BuildSystemPromptWithCache
// which already holds RLock or Lock).
func (cb *ContextBuilder) sourceFilesChangedLocked() bool {
	if cb.cachedAt.IsZero() {
		return true
	}
	// A new day brings other daily notes into the prompt.
	if cb.cachedDay != time.Now().Format("20060102") {
		return true
	}

	// Check tracked source files (bootstrap + memory).
	if slices.ContainsFunc(cb.sourcePaths(), cb.fileChangedSince) {
		return true
	}

	// --- Skill roots (workspace/global/builtin) ---
	//
	// For each root:
	// 1. Creation/deletion and root directory mtime changes are tracked by fileChangedSince.
	// 2. Nested file create/delete/mtime changes are tracked by the skill file snapshot.
	for _, root := range cb.skillRoots() {
		if cb.fileChangedSince(root) {
			return true
		}
	}
	if skillFilesChangedSince(cb.skillRoots(), cb.skillFilesAtCache) {
		return true
	}

	return false
}

// fileChangedSince returns true if a tracked source file has been modified,
// newly created, or deleted since the cache was built.
//
// Four cases:
//   - existed at cache time, exists now -> check mtime
//   - existed at cache time, gone now   -> changed (deleted)
//   - absent at cache time,  exists now -> changed (created)
//   - absent at cache time,  gone now   -> no change
func (cb *ContextBuilder) fileChangedSince(path string) bool {
	// Defensive: if existedAtCache was never initialized, treat as changed
	// so the cache rebuilds rather than silently serving stale data.
	if cb.existedAtCache == nil {
		return true
	}

	existedBefore := cb.existedAtCache[path]
	info, err := os.Stat(path)
	existsNow := err == nil

	if existedBefore != existsNow {
		return true // file was created or deleted
	}
	if !existsNow {
		return false // didn't exist before, doesn't exist now
	}
	return info.ModTime().After(cb.cachedAt)
}

// errWalkStop is a sentinel error used to stop filepath.WalkDir early.
// Using a dedicated error (instead of fs.SkipAll) makes the early-exit
// intent explicit and avoids the nilerr linter warning that would fire
// if the callback returned nil when its err parameter is non-nil.
var errWalkStop = errors.New("walk stop")

// skillFilesChangedSince compares the current recursive skill file tree
// against the cache-time snapshot. Any create/delete/mtime drift invalidates
// the cache.
func skillFilesChangedSince(skillRoots []string, filesAtCache map[string]time.Time) bool {
	// Defensive: if the snapshot was never initialized, force rebuild.
	if filesAtCache == nil {
		return true
	}

	// Check cached files still exist and keep the same mtime.
	for path, cachedMtime := range filesAtCache {
		info, err := os.Stat(path)
		if err != nil {
			// A previously tracked file disappeared (or became inaccessible):
			// either way, cached skill summary may now be stale.
			return true
		}
		if !info.ModTime().Equal(cachedMtime) {
			return true
		}
	}

	// Check no new files appeared under any skill root.
	changed := false
	for _, root := range skillRoots {
		if strings.TrimSpace(root) == "" {
			continue
		}

		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				// Treat unexpected walk errors as changed to avoid stale cache.
				if !os.IsNotExist(walkErr) {
					changed = true
					return errWalkStop
				}
				return nil
			}
			if d.IsDir() {
				return nil
			}
			if _, ok := filesAtCache[path]; !ok {
				changed = true
				return errWalkStop
			}
			return nil
		})

		if changed {
			return true
		}
		if err != nil && !errors.Is(err, errWalkStop) && !os.IsNotExist(err) {
			logger.DebugCF("agent", "skills walk error", map[string]any{"error": err.Error()})
			return true
		}
	}

	return false
}

func (cb *ContextBuilder) LoadBootstrapFiles() string {
	return cb.bootstrapContent(cb.LoadAgentDefinition())
}

// bootstrapContent renders the workspace definition files: the AGENT.md
// body, SOUL.md and USER.md.
func (cb *ContextBuilder) bootstrapContent(agentDefinition AgentContextDefinition) string {
	var sb strings.Builder

	if agentDefinition.Agent != nil {
		fmt.Fprintf(&sb, "## %s\n\n%s\n\n", agentDefinitionFile, agentDefinition.Agent.Body)
	}
	if agentDefinition.Soul != nil {
		fmt.Fprintf(
			&sb,
			"## %s\n\n%s\n\n",
			relativeWorkspacePath(cb.workspace, agentDefinition.Soul.Path),
			agentDefinition.Soul.Content,
		)
	}
	if agentDefinition.User != nil {
		fmt.Fprintf(&sb, "## %s\n\n%s\n\n", userDefinitionFile, agentDefinition.User.Content)
	}

	return sb.String()
}

func formatCurrentSenderLine(senderID, senderDisplayName string) string {
	senderID = strings.TrimSpace(senderID)
	senderDisplayName = strings.TrimSpace(senderDisplayName)

	switch {
	case senderDisplayName != "" && senderID != "":
		return fmt.Sprintf("Current sender: %s (ID: %s)", senderDisplayName, senderID)
	case senderDisplayName != "":
		return fmt.Sprintf("Current sender: %s", senderDisplayName)
	case senderID != "":
		return fmt.Sprintf("Current sender: %s", senderID)
	default:
		return ""
	}
}

// buildDynamicContext returns a short dynamic context string with per-request info.
// This changes every request (time, session) so it is NOT part of the cached prompt.
// LLM-side KV cache reuse is achieved by each provider adapter's native mechanism:
//   - Anthropic: per-block cache_control (ephemeral) on the static SystemParts block
//   - OpenAI: prompt_cache_key for prefix-based caching
//
// See: https://docs.anthropic.com/en/docs/build-with-claude/prompt-caching
// See: https://platform.openai.com/docs/guides/prompt-caching
func (cb *ContextBuilder) buildDynamicContext(
	channel, chatID, senderID, senderDisplayName string,
) string {
	now := time.Now().Format("2006-01-02 15:04 (Monday)")
	rt := fmt.Sprintf("%s %s, Go %s", runtime.GOOS, runtime.GOARCH, runtime.Version())

	var sb strings.Builder
	fmt.Fprintf(&sb, "## Current Time\n%s\n\n## Runtime\n%s", now, rt)

	if channel != "" && chatID != "" {
		fmt.Fprintf(&sb, "\n\n## Current Session\nChannel: %s\nChat ID: %s", channel, chatID)
	}
	if senderLine := formatCurrentSenderLine(senderID, senderDisplayName); senderLine != "" {
		fmt.Fprintf(&sb, "\n\n## Current Sender\n%s", senderLine)
	}

	return sb.String()
}

func (cb *ContextBuilder) BuildMessagesFromPrompt(req PromptBuildRequest) []providers.Message {
	messages := []providers.Message{}

	// The default static part (identity, bootstrap, skills, memory) is cached
	// locally to avoid repeated file I/O and string building on every call
	// (fixes issue #607). Profile-customized static prompts are built on demand.
	// Dynamic parts (time, session, summary) are appended per request unless the
	// profile suppresses Compa system context.
	// Everything is sent as a single system message for provider compatibility:
	// - Anthropic adapter extracts messages[0] (Role=="system") and maps its content
	//   to the top-level "system" parameter in the Messages API request. A single
	//   contiguous system block makes this extraction straightforward.
	// - Responses-style providers map only the first system message to their instructions field.
	// - OpenAI-compat passes messages through as-is.
	staticPrompt, contentBlocks := cb.buildSystemPromptForRequest(req)

	// Compose a single system message: static (cached) + dynamic + optional summary.
	// Keeping all system content in one message ensures every provider adapter can
	// extract it correctly (Anthropic adapter -> top-level system param,
	// Responses -> instructions field).
	//
	// SystemParts carries the same content as structured blocks so that
	// cache-aware adapters (Anthropic) can set per-block cache_control.
	// The static block is marked "ephemeral" — its prefix hash is stable
	// across requests, enabling LLM-side KV cache reuse.
	var stringParts []string
	if strings.TrimSpace(staticPrompt) != "" {
		stringParts = append(stringParts, staticPrompt)
	}

	promptParts := append([]PromptPart(nil), req.Overlays...)
	if !req.SuppressDefaultSystemPrompt && !req.SuppressSkillContext {
		activeSkills := append([]string(nil), req.ActiveSkills...)
		if len(req.AllowedSkills) > 0 {
			activeSkills = filterNamesByTurnProfile(activeSkills, req.AllowedSkills)
		}
		promptParts = append(promptParts, cb.buildActiveSkillsPromptParts(activeSkills)...)
	}
	if !req.SuppressDefaultSystemPrompt {
		if contributedParts, err := cb.promptRegistryOrDefault().Collect(context.Background(), req); err != nil {
			logger.WarnCF("agent", "Prompt contributor collection failed", map[string]any{
				"error": err.Error(),
			})
		} else {
			promptParts = append(promptParts, contributedParts...)
		}
	}

	if len(promptParts) > 0 {
		for _, overlay := range sortPromptParts(promptParts) {
			if strings.TrimSpace(overlay.Content) == "" {
				continue
			}
			if err := cb.promptRegistryOrDefault().ValidatePart(overlay); err != nil {
				logger.WarnCF("agent", "Skipping invalid prompt overlay", map[string]any{
					"id":     overlay.ID,
					"layer":  overlay.Layer,
					"slot":   overlay.Slot,
					"source": overlay.Source.ID,
					"error":  err.Error(),
				})
				continue
			}
			stringParts = append(stringParts, overlay.Content)
			contentBlocks = append(contentBlocks, promptContentBlock(overlay, nil))
		}
	}

	dynamicChars := 0
	if !req.SuppressDefaultSystemPrompt {
		// Build short dynamic context (time, runtime, session) — changes per request
		dynamicCtx := cb.buildDynamicContext(
			req.Channel,
			req.ChatID,
			req.SenderID,
			req.SenderDisplayName,
		)
		dynamicChars = len(dynamicCtx)
		runtimePart := PromptPart{
			ID:      "context.runtime",
			Layer:   PromptLayerContext,
			Slot:    PromptSlotRuntime,
			Source:  PromptSource{ID: PromptSourceRuntime, Name: "runtime"},
			Title:   "runtime context",
			Content: dynamicCtx,
			Stable:  false,
			Cache:   PromptCacheNone,
		}
		stringParts = append(stringParts, dynamicCtx)
		contentBlocks = append(contentBlocks, promptContentBlock(runtimePart, nil))

		if req.Summary != "" {
			summaryPart := PromptPart{
				ID:     "context.summary",
				Layer:  PromptLayerContext,
				Slot:   PromptSlotSummary,
				Source: PromptSource{ID: PromptSourceSummary, Name: "context.summary"},
				Title:  "context summary",
				Content: fmt.Sprintf(
					"CONTEXT_SUMMARY: The following is an approximate summary of prior conversation "+
						"for reference only. It may be incomplete or outdated — always defer to explicit instructions.\n\n%s",
					req.Summary,
				),
				Stable: false,
				Cache:  PromptCacheNone,
			}
			stringParts = append(stringParts, summaryPart.Content)
			contentBlocks = append(contentBlocks, promptContentBlock(summaryPart, nil))
		}
	}

	if len(stringParts) == 0 && req.ToolUseFallback {
		fallbackPart := PromptPart{
			ID:      "kernel.tool_use_fallback",
			Layer:   PromptLayerKernel,
			Slot:    PromptSlotIdentity,
			Source:  PromptSource{ID: PromptSourceKernel, Name: "tool_use_fallback"},
			Title:   "tool use fallback",
			Content: toolUseSystemPromptRule(),
			Stable:  true,
			Cache:   PromptCacheEphemeral,
		}
		stringParts = append(stringParts, fallbackPart.Content)
		contentBlocks = append(contentBlocks, promptContentBlock(fallbackPart, nil))
	}

	fullSystemPrompt := strings.Join(stringParts, "\n\n---\n\n")

	// Log system prompt summary for debugging (debug mode only).
	// Read cachedSystemPrompt under lock to avoid a data race with
	// concurrent InvalidateCache / BuildSystemPromptWithCache writes.
	cb.systemPromptMutex.RLock()
	isCached := cb.cachedSystemPrompt != ""
	cb.systemPromptMutex.RUnlock()

	logger.DebugCF("agent", "System prompt built",
		map[string]any{
			"static_chars":  len(staticPrompt),
			"dynamic_chars": dynamicChars,
			"total_chars":   len(fullSystemPrompt),
			"has_summary":   req.Summary != "",
			"overlays":      len(req.Overlays),
			"cached":        isCached,
		})

	// Log preview of system prompt (avoid logging huge content)
	preview := utils.Truncate(fullSystemPrompt, 500)
	logger.DebugCF("agent", "System prompt preview",
		map[string]any{
			"preview": preview,
		})

	history := sanitizeHistoryForProvider(req.History)

	// Single system message containing all context — compatible with all providers.
	// SystemParts enables cache-aware adapters to set per-block cache_control;
	// Content is the concatenated fallback for adapters that don't read SystemParts.
	if strings.TrimSpace(fullSystemPrompt) != "" {
		messages = append(messages, providers.Message{
			Role:        "system",
			Content:     fullSystemPrompt,
			SystemParts: contentBlocks,
		})
	}

	// Add conversation history
	messages = append(messages, history...)

	// Add current user message. Media-only turns must still be preserved so
	// multimodal providers receive the uploaded image even when the user sends
	// no accompanying text.
	if strings.TrimSpace(req.CurrentMessage) != "" || len(req.Media) > 0 {
		messages = append(messages, userPromptMessage(req.CurrentMessage, req.Media))
	}
	if len(messages) == 0 {
		messages = append(messages, userPromptMessage("", nil))
	}

	return messages
}

func sanitizeHistoryForProvider(history []providers.Message) []providers.Message {
	if len(history) == 0 {
		return history
	}

	sanitized := make([]providers.Message, 0, len(history))
	for _, msg := range history {
		switch msg.Role {
		case "system":
			// Drop system messages from history. BuildMessagesFromPrompt always
			// constructs its own single system message (static + dynamic +
			// summary); extra system messages would break providers that
			// only accept one (Anthropic, Responses).
			logger.DebugCF("agent", "Dropping system message from history", map[string]any{})
			continue

		case "tool":
			if len(sanitized) == 0 {
				logger.DebugCF("agent", "Dropping orphaned leading tool message", map[string]any{})
				continue
			}
			// Walk backwards to find the nearest assistant message,
			// skipping over any preceding tool messages (multi-tool-call case).
			foundAssistant := false
			for i := len(sanitized) - 1; i >= 0; i-- {
				if sanitized[i].Role == "tool" {
					continue
				}
				if sanitized[i].Role == "assistant" && len(sanitized[i].ToolCalls) > 0 {
					foundAssistant = true
				}
				break
			}
			if !foundAssistant {
				logger.DebugCF("agent", "Dropping orphaned tool message", map[string]any{})
				continue
			}
			sanitized = append(sanitized, msg)

		case "assistant":
			if len(msg.ToolCalls) > 0 {
				if len(sanitized) == 0 {
					logger.DebugCF(
						"agent",
						"Dropping assistant tool-call turn at history start",
						map[string]any{},
					)
					continue
				}
				prev := sanitized[len(sanitized)-1]
				if prev.Role != "user" && prev.Role != "tool" {
					logger.DebugCF(
						"agent",
						"Dropping assistant tool-call turn with invalid predecessor",
						map[string]any{"prev_role": prev.Role},
					)
					continue
				}
			}
			sanitized = append(sanitized, msg)

		default:
			sanitized = append(sanitized, msg)
		}
	}

	// Second pass: ensure every assistant message with tool_calls has matching
	// tool result messages following it. This is required by strict providers
	// like DeepSeek that enforce: "An assistant message with 'tool_calls' must
	// be followed by tool messages responding to each 'tool_call_id'."
	//
	// Deduplication is scoped to the contiguous tool-result block that follows a
	// single assistant tool-call message. Some providers legitimately reuse call
	// IDs across separate turns (for example "call_0"), so global deduplication
	// would incorrectly delete later valid tool results and leave an
	// assistant(tool_calls) -> assistant sequence behind.
	final := make([]providers.Message, 0, len(sanitized))
	for i := 0; i < len(sanitized); i++ {
		msg := sanitized[i]

		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			expected := make(map[string]bool, len(msg.ToolCalls))
			invalidToolCallID := false
			for _, tc := range msg.ToolCalls {
				if tc.ID == "" {
					invalidToolCallID = true
					continue
				}
				expected[tc.ID] = false
			}

			block := make([]providers.Message, 0, len(expected))
			seenInBlock := make(map[string]bool, len(expected))
			j := i + 1
			for ; j < len(sanitized); j++ {
				next := sanitized[j]
				if next.Role != "tool" {
					break
				}
				if next.ToolCallID == "" {
					logger.DebugCF(
						"agent",
						"Dropping tool result without tool_call_id",
						map[string]any{},
					)
					continue
				}
				if _, ok := expected[next.ToolCallID]; !ok {
					logger.DebugCF("agent", "Dropping unexpected tool result", map[string]any{
						"tool_call_id": next.ToolCallID,
					})
					continue
				}
				if seenInBlock[next.ToolCallID] {
					logger.DebugCF(
						"agent",
						"Dropping duplicate tool result in tool block",
						map[string]any{
							"tool_call_id": next.ToolCallID,
						},
					)
					continue
				}
				seenInBlock[next.ToolCallID] = true
				expected[next.ToolCallID] = true
				block = append(block, next)
			}

			allFound := !invalidToolCallID
			if invalidToolCallID {
				logger.DebugCF(
					"agent",
					"Dropping assistant message with empty tool_call_id",
					map[string]any{},
				)
			}
			for toolCallID, found := range expected {
				if !found {
					allFound = false
					logger.DebugCF(
						"agent",
						"Dropping assistant message with incomplete tool results",
						map[string]any{
							"missing_tool_call_id": toolCallID,
							"expected_count":       len(expected),
							"found_count":          len(block),
						},
					)
					break
				}
			}

			if !allFound {
				i = j - 1
				continue
			}

			final = append(final, msg)
			final = append(final, block...)
			i = j - 1
			continue
		}

		if msg.Role == "tool" {
			logger.DebugCF(
				"agent",
				"Dropping orphaned tool message after validation",
				map[string]any{
					"tool_call_id": msg.ToolCallID,
				},
			)
			continue
		}

		final = append(final, msg)
	}

	return final
}

func (cb *ContextBuilder) buildActiveSkillsContext(skillNames []string) string {
	ordered := cb.ResolveActiveSkillsForContext(skillNames)
	if len(ordered) == 0 {
		return ""
	}

	content := cb.skillsLoader.LoadSkillsForContext(ordered)
	if strings.TrimSpace(content) == "" {
		return ""
	}

	return fmt.Sprintf(`# Active Skills

The following skills are active for this request. Follow them when relevant.

%s`, content)
}

func (cb *ContextBuilder) ResolveActiveSkillsForContext(skillNames []string) []string {
	if cb.skillsLoader == nil || len(skillNames) == 0 {
		return nil
	}

	var ordered []string
	seen := make(map[string]struct{}, len(skillNames))
	for _, name := range skillNames {
		canonical, ok := cb.ResolveSkillName(name)
		if !ok {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		seen[canonical] = struct{}{}
		ordered = append(ordered, canonical)
	}
	if len(ordered) == 0 {
		return nil
	}
	return ordered
}

func (cb *ContextBuilder) buildActiveSkillsPromptParts(skillNames []string) []PromptPart {
	skillsText := cb.buildActiveSkillsContext(skillNames)
	if strings.TrimSpace(skillsText) == "" {
		return nil
	}

	return []PromptPart{
		{
			ID:      "capability.active_skills",
			Layer:   PromptLayerCapability,
			Slot:    PromptSlotActiveSkill,
			Source:  PromptSource{ID: PromptSourceActiveSkills, Name: "skill:active"},
			Title:   "active skills",
			Content: skillsText,
			Stable:  false,
			Cache:   PromptCacheNone,
		},
	}
}

func (cb *ContextBuilder) ListSkillNames() []string {
	if cb.skillsLoader == nil {
		return nil
	}

	allSkills := cb.skillsLoader.ListSkills()
	names := make([]string, 0, len(allSkills))
	for _, skill := range allSkills {
		names = append(names, skill.Name)
	}
	return names
}

func (cb *ContextBuilder) ResolveSkillName(name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || cb.skillsLoader == nil {
		return "", false
	}

	for _, skill := range cb.skillsLoader.ListSkills() {
		if strings.EqualFold(skill.Name, name) {
			return skill.Name, true
		}
	}

	return "", false
}

// GetSkillsInfo returns information about loaded skills.
func (cb *ContextBuilder) GetSkillsInfo() map[string]any {
	allSkills := cb.skillsLoader.ListSkills()
	skillNames := make([]string, 0, len(allSkills))
	for _, s := range allSkills {
		skillNames = append(skillNames, s.Name)
	}
	return map[string]any{
		"total":     len(allSkills),
		"available": len(allSkills),
		"names":     skillNames,
	}
}
