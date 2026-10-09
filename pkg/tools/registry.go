package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xibodev/compa/v4/pkg/logger"
	"github.com/xibodev/compa/v4/pkg/media"
	"github.com/xibodev/compa/v4/pkg/providers"
)

type ToolEntry struct {
	Tool   Tool
	IsCore bool
	TTL    int
	// Extension marks a tool registered through RegisterExtension, which a
	// built-in tool of the same name takes the place of.
	Extension bool
}

type ToolRegistry struct {
	tools      map[string]*ToolEntry
	mu         sync.RWMutex
	version    atomic.Uint64 // incremented on Register/RegisterHidden for cache invalidation
	mediaStore media.MediaStore
	allowlist  map[string]struct{}
	// sessionUnlocks holds, per session key, the hidden tools that session
	// unlocked through tool discovery and how many rounds they stay visible.
	sessionUnlocks map[string]map[string]int
	// sensitiveFilter, when set, removes secrets from the arguments and
	// results written to the debug log.
	sensitiveFilter func(string) string
}

type mediaStoreAware interface {
	SetMediaStore(store media.MediaStore)
}

// availabilityReporter is implemented by tools that only work on some
// platforms; a tool reporting false is not registered.
type availabilityReporter interface {
	Available() bool
}

const (
	// maxUnlockSessions bounds how many sessions' discovery unlocks are kept.
	maxUnlockSessions = 256
	// maxLoggedArgsBytes bounds the arguments written to the debug log.
	maxLoggedArgsBytes = 16 * 1024
	// maxLoggedErrorChars bounds a failed tool's result in the error log.
	maxLoggedErrorChars = 500
)

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools:          make(map[string]*ToolEntry),
		sessionUnlocks: make(map[string]map[string]int),
	}
}

// SetAllowlist restricts registrations to the provided runtime tool names.
// A nil slice means "allow all". An empty-but-non-nil slice means "allow none".
func (r *ToolRegistry) SetAllowlist(names []string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if names == nil {
		r.allowlist = nil
		return
	}

	allowlist := make(map[string]struct{}, len(names))
	for _, name := range names {
		trimmed := strings.ToLower(strings.TrimSpace(name))
		if trimmed == "" {
			continue
		}
		allowlist[trimmed] = struct{}{}
	}
	r.allowlist = allowlist
}

// SetSensitiveDataFilter sets the function that removes secrets from the
// tool arguments and failure results the registry logs.
func (r *ToolRegistry) SetSensitiveDataFilter(filter func(string) string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sensitiveFilter = filter
}

// Register adds a visible tool. A name that is already registered keeps its
// first tool -- a later tool must not silently take the place of another --
// except for a tool added through RegisterExtension, which a built-in tool
// replaces.
func (r *ToolRegistry) Register(tool Tool) {
	r.register(tool, true, false)
}

// RegisterHidden saves hidden tools (visible only via TTL). A name that is
// already registered keeps its first tool.
func (r *ToolRegistry) RegisterHidden(tool Tool) {
	r.register(tool, false, false)
}

// RegisterExtension adds a visible tool contributed from outside Compa, such
// as a module's. It never replaces a registered tool, and a built-in tool
// registered later under the same name replaces it.
func (r *ToolRegistry) RegisterExtension(tool Tool) {
	r.register(tool, true, true)
}

func (r *ToolRegistry) register(tool Tool, visible, extension bool) {
	if tool == nil {
		return
	}
	kind := "core"
	if !visible {
		kind = "hidden"
	} else if extension {
		kind = "extension"
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	name := tool.Name()
	if !r.toolAllowedLocked(name) {
		logger.DebugCF("tools", "Skipped "+kind+" tool registration by agent allowlist",
			map[string]any{"name": name})
		return
	}
	if reporter, ok := tool.(availabilityReporter); ok && !reporter.Available() {
		logger.InfoCF("tools", "Tool not registered: not supported on this platform",
			map[string]any{"name": name})
		return
	}
	if existing, exists := r.tools[name]; exists {
		if !existing.Extension || extension || !visible {
			logger.ErrorCF("tools", "Tool name already registered; keeping the first tool",
				map[string]any{"name": name, "refused": kind})
			return
		}
		logger.ErrorCF("tools", "Built-in tool replaces an extension tool of the same name",
			map[string]any{"name": name})
	}
	r.tools[name] = &ToolEntry{
		Tool:      tool,
		IsCore:    visible,
		TTL:       0, // Core tools do not use TTL
		Extension: extension,
	}
	if aware, ok := tool.(mediaStoreAware); ok && r.mediaStore != nil {
		aware.SetMediaStore(r.mediaStore)
	}
	r.version.Add(1)
	logger.DebugCF("tools", "Registered "+kind+" tool", map[string]any{"name": name})
}

// Unregister removes a tool, reporting whether it was registered.
func (r *ToolRegistry) Unregister(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.tools[name]; !ok {
		return false
	}
	delete(r.tools, name)
	for _, unlocked := range r.sessionUnlocks {
		delete(unlocked, name)
	}
	r.version.Add(1)
	return true
}

// SetMediaStore injects a MediaStore into all registered tools that can
// consume it, and remembers it for future registrations.
func (r *ToolRegistry) SetMediaStore(store media.MediaStore) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.mediaStore = store
	for _, entry := range r.tools {
		if aware, ok := entry.Tool.(mediaStoreAware); ok {
			aware.SetMediaStore(store)
		}
	}
}

// PromoteTools atomically sets the TTL for multiple non-core tools, for every
// session of the agent. Discovery tools unlock per session instead (see
// PromoteToolsForSession). Setting all TTLs under one lock prevents a
// concurrent TickTTL from decrementing between promotions.
func (r *ToolRegistry) PromoteTools(names []string, ttl int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	promoted := 0
	for _, name := range names {
		if entry, exists := r.tools[name]; exists {
			if !entry.IsCore {
				entry.TTL = ttl
				promoted++
			}
		}
	}
	logger.DebugCF(
		"tools",
		"PromoteTools completed",
		map[string]any{"requested": len(names), "promoted": promoted, "ttl": ttl},
	)
}

// PromoteToolsForSession unlocks hidden tools for one session only, for ttl
// rounds of that session. An empty session key promotes for every session.
func (r *ToolRegistry) PromoteToolsForSession(sessionKey string, names []string, ttl int) {
	if sessionKey == "" {
		r.PromoteTools(names, ttl)
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	unlocked := r.sessionUnlocks[sessionKey]
	if unlocked == nil {
		r.trimUnlockSessionsLocked()
		unlocked = make(map[string]int)
		r.sessionUnlocks[sessionKey] = unlocked
	}
	promoted := 0
	for _, name := range names {
		if entry, exists := r.tools[name]; exists && !entry.IsCore && ttl > 0 {
			unlocked[name] = ttl
			promoted++
		}
	}
	if len(unlocked) == 0 {
		delete(r.sessionUnlocks, sessionKey)
	}
	logger.DebugCF("tools", "PromoteToolsForSession completed",
		map[string]any{"requested": len(names), "promoted": promoted, "ttl": ttl})
}

// trimUnlockSessionsLocked makes room for one more session's unlocks.
func (r *ToolRegistry) trimUnlockSessionsLocked() {
	for key := range r.sessionUnlocks {
		if len(r.sessionUnlocks) < maxUnlockSessions {
			return
		}
		delete(r.sessionUnlocks, key)
	}
}

// TickTTL decreases TTL only for non-core tools: the TTLs set for every
// session and those of each session.
func (r *ToolRegistry) TickTTL() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tickGlobalLocked()
	for key := range r.sessionUnlocks {
		r.tickSessionLocked(key)
	}
}

// TickTTLForSession ends one round of a session: the tools that session
// unlocked, and those unlocked for every session, count down by one.
func (r *ToolRegistry) TickTTLForSession(sessionKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tickGlobalLocked()
	if sessionKey != "" {
		r.tickSessionLocked(sessionKey)
	}
}

func (r *ToolRegistry) tickGlobalLocked() {
	for _, entry := range r.tools {
		if !entry.IsCore && entry.TTL > 0 {
			entry.TTL--
		}
	}
}

func (r *ToolRegistry) tickSessionLocked(sessionKey string) {
	unlocked := r.sessionUnlocks[sessionKey]
	for name, ttl := range unlocked {
		if ttl <= 1 {
			delete(unlocked, name)
		} else {
			unlocked[name] = ttl - 1
		}
	}
	if len(unlocked) == 0 {
		delete(r.sessionUnlocks, sessionKey)
	}
}

// Version returns the current registry version (atomically).
func (r *ToolRegistry) Version() uint64 {
	return r.version.Load()
}

func (r *ToolRegistry) toolAllowedLocked(name string) bool {
	if r.allowlist == nil {
		return true
	}
	if isToolDiscoveryToolName(name) {
		// Discovery tools are part of the MCP control plane: they must remain
		// available whenever configured so deferred MCP tools can still be
		// unlocked. Per-agent allowlists still apply to the hidden MCP tools
		// themselves during RegisterHidden.
		return true
	}
	_, ok := r.allowlist[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// unlockedNamesLocked returns the hidden tools some session has unlocked.
func (r *ToolRegistry) unlockedNamesLocked() map[string]struct{} {
	if len(r.sessionUnlocks) == 0 {
		return nil
	}
	names := make(map[string]struct{})
	for _, unlocked := range r.sessionUnlocks {
		for name, ttl := range unlocked {
			if ttl > 0 {
				names[name] = struct{}{}
			}
		}
	}
	return names
}

// visibleLocked reports whether a tool is offered and callable: core tools
// always, hidden tools while unlocked. With a session key, only that
// session's unlocks count; without one, any session's do.
func (r *ToolRegistry) visibleLocked(
	name string,
	entry *ToolEntry,
	sessionKey string,
	anySession map[string]struct{},
) bool {
	if entry.IsCore || entry.TTL > 0 {
		return true
	}
	if sessionKey != "" {
		return r.sessionUnlocks[sessionKey][name] > 0
	}
	_, ok := anySession[name]
	return ok
}

// HasRegistered reports whether a tool name is present in the registry,
// including hidden tools whose TTL is currently zero.
func (r *ToolRegistry) HasRegistered(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.tools[name]
	return ok
}

// registered returns the tool registered as name, visible or not.
func (r *ToolRegistry) registered(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	return entry.Tool, true
}

// HiddenToolSnapshot holds a consistent snapshot of hidden tools and the
// registry version at which it was taken. Used by BM25SearchTool cache.
type HiddenToolSnapshot struct {
	Docs    []HiddenToolDoc
	Version uint64
}

// HiddenToolDoc is a lightweight representation of a hidden tool for search indexing.
type HiddenToolDoc struct {
	Name        string
	Description string
}

// SnapshotHiddenTools returns all non-core tools and the current registry
// version under a single read-lock, guaranteeing consistency between the
// two values.
func (r *ToolRegistry) SnapshotHiddenTools() HiddenToolSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	docs := make([]HiddenToolDoc, 0, len(r.tools))
	for name, entry := range r.tools {
		if !entry.IsCore {
			docs = append(docs, HiddenToolDoc{
				Name:        name,
				Description: entry.Tool.Description(),
			})
		}
	}
	return HiddenToolSnapshot{
		Docs:    docs,
		Version: r.version.Load(),
	}
}

// Get returns a callable tool: a core tool, or a hidden tool that is
// unlocked for every session or for any one session.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.tools[name]
	if !ok {
		return nil, false
	}
	// Hidden tools with expired TTL are not callable.
	if !r.visibleLocked(name, entry, "", r.unlockedNamesLocked()) {
		return nil, false
	}
	return entry.Tool, true
}

func (r *ToolRegistry) Execute(ctx context.Context, name string, args map[string]any) *ToolResult {
	return r.ExecuteWithContext(ctx, name, args, "", "", nil)
}

// ExecuteWithContext executes a tool with channel/chatID context and optional async callback.
// If the tool implements AsyncExecutor and a non-nil callback is provided,
// ExecuteAsync is called instead of Execute — the callback is a parameter,
// never stored as mutable state on the tool.
func (r *ToolRegistry) ExecuteWithContext(
	ctx context.Context,
	name string,
	args map[string]any,
	channel, chatID string,
	asyncCallback AsyncCallback,
) *ToolResult {
	// Arguments carry user data, file contents and sometimes credentials:
	// INFO records only which arguments came and how large they were.
	logger.InfoCF("tool", "Tool execution started",
		map[string]any{
			"tool":      name,
			"arg_bytes": argumentSizes(args),
		})
	if logger.GetLevel() <= logger.DEBUG {
		logger.DebugCF("tool", "Tool arguments",
			map[string]any{
				"tool": name,
				"args": r.loggableArgs(args),
			})
	}

	tool, ok := r.Get(name)
	if !ok {
		logger.ErrorCF("tool", "Tool not found",
			map[string]any{
				"tool": name,
			})
		return ErrorResult(
			fmt.Sprintf("tool %q not found", name),
		).WithError(fmt.Errorf("tool not found"))
	}

	// Validate arguments against the tool's declared schema.
	if err := validateToolArgs(tool.Parameters(), args); err != nil {
		logger.WarnCF("tool", "Tool argument validation failed",
			map[string]any{"tool": name, "error": r.loggableText(err.Error(), maxLoggedErrorChars)})
		return ErrorResult(fmt.Sprintf("invalid arguments for tool %q: %s", name, err)).
			WithError(fmt.Errorf("argument validation failed: %w", err))
	}

	// Inject channel/chatID into ctx so tools read them via ToolChannel(ctx)/ToolChatID(ctx).
	// Always inject — tools validate what they require.
	ctx = WithToolContext(ctx, channel, chatID)

	// If tool implements AsyncExecutor and callback is provided, use ExecuteAsync.
	// The callback is a call parameter, not mutable state on the tool.
	var result *ToolResult
	start := time.Now()

	// Use recover to catch any panics during tool execution
	// This prevents tool crashes from killing the entire agent
	func() {
		defer func() {
			if re := recover(); re != nil {
				logger.RecoverPanicNoExit(re)
				errMsg := fmt.Sprintf("Tool '%s' crashed with panic: %v", name, re)
				logger.ErrorCF("tool", "Tool execution panic recovered",
					map[string]any{
						"tool":  name,
						"panic": fmt.Sprintf("%v", re),
					})
				result = &ToolResult{
					ForLLM:  errMsg,
					ForUser: errMsg,
					IsError: true,
					Err:     fmt.Errorf("panic: %v", re),
				}
			}
		}()

		if asyncExec, ok := tool.(AsyncExecutor); ok && asyncCallback != nil {
			logger.DebugCF("tool", "Executing async tool via ExecuteAsync",
				map[string]any{
					"tool": name,
				})
			result = asyncExec.ExecuteAsync(ctx, args, asyncCallback)
		} else {
			result = tool.Execute(ctx, args)
		}
	}()

	// Handle nil result (should not happen, but defensive)
	if result == nil {
		result = &ToolResult{
			ForLLM:  fmt.Sprintf("Tool '%s' returned nil result unexpectedly", name),
			ForUser: fmt.Sprintf("Tool '%s' returned nil result unexpectedly", name),
			IsError: true,
			Err:     fmt.Errorf("nil result from tool"),
		}
	}

	result = normalizeToolResult(result, name, r.mediaStore, channel, chatID)

	duration := time.Since(start)

	// Log based on result type
	if result.IsError {
		logger.ErrorCF("tool", "Tool execution failed",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
				"error":    r.loggableText(result.ForLLM, maxLoggedErrorChars),
			})
	} else if result.Async {
		logger.InfoCF("tool", "Tool started (async)",
			map[string]any{
				"tool":     name,
				"duration": duration.Milliseconds(),
			})
	} else {
		logger.InfoCF("tool", "Tool execution completed",
			map[string]any{
				"tool":          name,
				"duration_ms":   duration.Milliseconds(),
				"result_length": len(result.ContentForLLM()),
			})
	}

	return result
}

// argumentSizes maps each argument name to the size of its JSON value.
func argumentSizes(args map[string]any) map[string]int {
	sizes := make(map[string]int, len(args))
	for key, value := range args {
		data, err := json.Marshal(value)
		if err != nil {
			sizes[key] = -1
			continue
		}
		sizes[key] = len(data)
	}
	return sizes
}

// loggableArgs renders the arguments for the debug log: as JSON, without the
// secrets the sensitive-data filter knows, and bounded in size.
func (r *ToolRegistry) loggableArgs(args map[string]any) string {
	data, err := json.Marshal(args)
	if err != nil {
		return fmt.Sprintf("<unencodable arguments: %v>", err)
	}
	return r.loggableText(string(data), maxLoggedArgsBytes)
}

// loggableText filters secrets from text and bounds it to limit characters.
func (r *ToolRegistry) loggableText(text string, limit int) string {
	r.mu.RLock()
	filter := r.sensitiveFilter
	r.mu.RUnlock()
	if filter != nil {
		text = filter(text)
	}
	if cut, rest := runePrefix(text, limit); rest > 0 {
		return fmt.Sprintf("%s... (%d more chars)", cut, rest)
	}
	return text
}

// sortedToolNames returns tool names in sorted order for deterministic iteration.
// This is critical for KV cache stability: non-deterministic map iteration would
// produce different system prompts and tool definitions on each call, invalidating
// the LLM's prefix cache even when no tools have changed.
func (r *ToolRegistry) sortedToolNames() []string {
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (r *ToolRegistry) GetDefinitions() []map[string]any {
	r.mu.RLock()
	defer r.mu.RUnlock()

	anySession := r.unlockedNamesLocked()
	sorted := r.sortedToolNames()
	definitions := make([]map[string]any, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !r.visibleLocked(name, entry, "", anySession) {
			continue
		}

		definitions = append(definitions, ToolToSchema(r.tools[name].Tool))
	}
	return definitions
}

// ToProviderDefs converts tool definitions to provider-compatible format.
// This is the format expected by LLM provider APIs. Hidden tools unlocked by
// any session are included; ToProviderDefsForSession limits them to one.
func (r *ToolRegistry) ToProviderDefs() []providers.ToolDefinition {
	return r.ToProviderDefsForSession("")
}

// ToProviderDefsForSession returns the provider definitions offered to one
// session: core tools, hidden tools unlocked for every session, and those the
// session itself unlocked. An empty key includes every session's unlocks.
func (r *ToolRegistry) ToProviderDefsForSession(sessionKey string) []providers.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var anySession map[string]struct{}
	if sessionKey == "" {
		anySession = r.unlockedNamesLocked()
	}
	sorted := r.sortedToolNames()
	definitions := make([]providers.ToolDefinition, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !r.visibleLocked(name, entry, sessionKey, anySession) {
			continue
		}

		schema := ToolToSchema(entry.Tool)

		// Safely extract nested values with type checks
		fn, ok := schema["function"].(map[string]any)
		if !ok {
			continue
		}

		name, _ := fn["name"].(string)
		desc, _ := fn["description"].(string)
		params, _ := fn["parameters"].(map[string]any)
		metadata := promptMetadataForTool(entry.Tool)

		definitions = append(definitions, providers.ToolDefinition{
			Type: "function",
			Function: providers.ToolFunctionDefinition{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
			PromptLayer:  metadata.Layer,
			PromptSlot:   metadata.Slot,
			PromptSource: metadata.Source,
		})
	}
	return definitions
}

func promptMetadataForTool(tool Tool) PromptMetadata {
	metadata := PromptMetadata{
		Layer:  ToolPromptLayerCapability,
		Slot:   ToolPromptSlotTooling,
		Source: ToolPromptSourceRegistry,
	}
	if provider, ok := tool.(PromptMetadataProvider); ok {
		provided := provider.PromptMetadata()
		if provided.Layer != "" {
			metadata.Layer = provided.Layer
		}
		if provided.Slot != "" {
			metadata.Slot = provided.Slot
		}
		if provided.Source != "" {
			metadata.Source = provided.Source
		}
	}
	return metadata
}

// List returns a list of all registered tool names.
func (r *ToolRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.sortedToolNames()
}

// Clone creates an independent copy of the registry containing the same tool
// entries (shallow copy of each ToolEntry). This is used to give subagents a
// snapshot of the parent agent's tools without sharing the same registry —
// tools registered on the parent after cloning (e.g. spawn, spawn_status)
// will NOT be visible to the clone, preventing recursive subagent spawning.
// The version counter is reset to 0 in the clone as it's a new independent registry.
func (r *ToolRegistry) Clone() *ToolRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := &ToolRegistry{
		tools:           make(map[string]*ToolEntry, len(r.tools)),
		mediaStore:      r.mediaStore,
		sessionUnlocks:  make(map[string]map[string]int, len(r.sessionUnlocks)),
		sensitiveFilter: r.sensitiveFilter,
	}
	if r.allowlist != nil {
		clone.allowlist = make(map[string]struct{}, len(r.allowlist))
		for name := range r.allowlist {
			clone.allowlist[name] = struct{}{}
		}
	}
	for name, entry := range r.tools {
		clone.tools[name] = &ToolEntry{
			Tool:      entry.Tool,
			IsCore:    entry.IsCore,
			TTL:       entry.TTL,
			Extension: entry.Extension,
		}
	}
	for key, unlocked := range r.sessionUnlocks {
		copied := make(map[string]int, len(unlocked))
		for name, ttl := range unlocked {
			copied[name] = ttl
		}
		clone.sessionUnlocks[key] = copied
	}
	return clone
}

// Count returns the number of registered tools.
func (r *ToolRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// GetSummaries returns human-readable summaries of all registered tools.
// Returns a slice of "name - description" strings.
func (r *ToolRegistry) GetSummaries() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	anySession := r.unlockedNamesLocked()
	sorted := r.sortedToolNames()
	summaries := make([]string, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		if !r.visibleLocked(name, entry, "", anySession) {
			continue
		}

		summaries = append(
			summaries,
			fmt.Sprintf("- `%s` - %s", entry.Tool.Name(), entry.Tool.Description()),
		)
	}
	return summaries
}

// GetAll returns all registered tools (both core and non-core with TTL > 0).
// Used by SubTurn to inherit parent's tool set.
func (r *ToolRegistry) GetAll() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	anySession := r.unlockedNamesLocked()
	sorted := r.sortedToolNames()
	tools := make([]Tool, 0, len(sorted))
	for _, name := range sorted {
		entry := r.tools[name]

		// Include core tools and non-core tools with active TTL
		if r.visibleLocked(name, entry, "", anySession) {
			tools = append(tools, entry.Tool)
		}
	}
	return tools
}
