package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/xibodev/compa/pkg/logger"
	"github.com/xibodev/compa/pkg/utils"
)

const (
	MaxRegexPatternLength = 200
	RegexSearchToolName   = "tool_search_tool_regex"
	BM25SearchToolName    = "tool_search_tool_bm25"
)

type hiddenToolsKey struct{}

// WithHiddenTools returns ctx for a call of a turn that is not offered the
// tools hidden reports (the approval policy's hide): tool discovery leaves
// them out. hidden runs under the registry's lock and must not use it.
func WithHiddenTools(ctx context.Context, hidden func(name string, tool Tool) bool) context.Context {
	return context.WithValue(ctx, hiddenToolsKey{}, hidden)
}

func hiddenToolsFrom(ctx context.Context) func(name string, tool Tool) bool {
	hidden, _ := ctx.Value(hiddenToolsKey{}).(func(name string, tool Tool) bool)
	return hidden
}

type RegexSearchTool struct {
	registry         *ToolRegistry
	ttl              int
	maxSearchResults int
}

func NewRegexSearchTool(r *ToolRegistry, ttl int, maxSearchResults int) *RegexSearchTool {
	return &RegexSearchTool{registry: r, ttl: ttl, maxSearchResults: maxSearchResults}
}

func (t *RegexSearchTool) Name() string {
	return RegexSearchToolName
}

func (t *RegexSearchTool) Description() string {
	return "Search available hidden tools on-demand using a regex pattern. Returns JSON schemas of discovered tools."
}

func (t *RegexSearchTool) PromptMetadata() PromptMetadata {
	return PromptMetadata{
		Layer:  ToolPromptLayerCapability,
		Slot:   ToolPromptSlotTooling,
		Source: ToolPromptSourceDiscovery,
	}
}

func (t *RegexSearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "Regex pattern to match tool name or description",
			},
		},
		"required": []string{"pattern"},
	}
}

func (t *RegexSearchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	pattern, ok := args["pattern"].(string)
	if !ok || strings.TrimSpace(pattern) == "" {
		// An empty string regex (?i) will match every hidden tool,
		// dumping massive payloads into the context and burning tokens.
		return ErrorResult("Missing or invalid 'pattern' argument. Must be a non-empty string.")
	}

	if len(pattern) > MaxRegexPatternLength {
		logger.WarnCF("discovery", "Regex pattern rejected (too long)", map[string]any{"len": len(pattern)})
		return ErrorResult(fmt.Sprintf("Pattern too long: max %d characters allowed", MaxRegexPatternLength))
	}

	logger.DebugCF("discovery", "Regex search", map[string]any{"pattern": pattern})

	res, err := t.registry.searchRegex(pattern, t.maxSearchResults, hiddenToolsFrom(ctx))
	if err != nil {
		logger.DebugCF("discovery", "Invalid regex pattern", map[string]any{"pattern": pattern, "error": err.Error()})
		return ErrorResult(fmt.Sprintf("Invalid regex pattern syntax: %v. Please fix your regex and try again.", err))
	}

	logger.InfoCF("discovery", "Regex search completed", map[string]any{"results": len(res)})
	return formatDiscoveryResponse(t.registry, ToolSessionKey(ctx), res, t.ttl)
}

type BM25SearchTool struct {
	registry         *ToolRegistry
	ttl              int
	maxSearchResults int

	// Cache: rebuilt only when the registry version changes. The pointer is
	// read without the lock, so the engine and its version travel together.
	cacheMu sync.Mutex
	cache   atomic.Pointer[bm25Cache]
}

// bm25Cache is a built engine and the registry version it was built at. A nil
// engine means there were no hidden tools at that version.
type bm25Cache struct {
	engine  *bm25CachedEngine
	version uint64
}

func NewBM25SearchTool(r *ToolRegistry, ttl int, maxSearchResults int) *BM25SearchTool {
	return &BM25SearchTool{registry: r, ttl: ttl, maxSearchResults: maxSearchResults}
}

func (t *BM25SearchTool) Name() string {
	return BM25SearchToolName
}

func (t *BM25SearchTool) Description() string {
	return "Search available hidden tools on-demand using natural language query describing the action you need to perform. Returns JSON schemas of discovered tools."
}

func (t *BM25SearchTool) PromptMetadata() PromptMetadata {
	return PromptMetadata{
		Layer:  ToolPromptLayerCapability,
		Slot:   ToolPromptSlotTooling,
		Source: ToolPromptSourceDiscovery,
	}
}

func (t *BM25SearchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query",
			},
		},
		"required": []string{"query"},
	}
}

func (t *BM25SearchTool) Execute(ctx context.Context, args map[string]any) *ToolResult {
	query, ok := args["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		// An empty string query will match every hidden tool,
		// dumping massive payloads into the context and burning tokens.
		return ErrorResult("Missing or invalid 'query' argument. Must be a non-empty string.")
	}

	logger.DebugCF("discovery", "BM25 search", map[string]any{"query": query})

	cached := t.getOrBuildEngine()
	if cached == nil {
		logger.DebugCF("discovery", "BM25 search: no hidden tools available", nil)
		return SilentResult("No tools found matching the query.")
	}

	// Leaving out the tools the turn may not see takes ranking them all.
	hidden := hiddenToolsFrom(ctx)
	limit := t.maxSearchResults
	if hidden != nil {
		limit = cached.size
	}
	ranked := cached.engine.Search(query, limit)
	results := make([]ToolSearchResult, 0, len(ranked))
	for _, r := range ranked {
		if len(results) >= t.maxSearchResults {
			break
		}
		if hidden != nil {
			if tool, ok := t.registry.registered(r.Document.Name); !ok || hidden(r.Document.Name, tool) {
				continue
			}
		}
		results = append(results, ToolSearchResult{
			Name:        r.Document.Name,
			Description: r.Document.Description,
		})
	}
	if len(results) == 0 {
		logger.DebugCF("discovery", "BM25 search: no matches", map[string]any{"query": query})
		return SilentResult("No tools found matching the query.")
	}

	logger.InfoCF("discovery", "BM25 search completed", map[string]any{"results": len(results)})
	return formatDiscoveryResponse(t.registry, ToolSessionKey(ctx), results, t.ttl)
}

// ToolSearchResult represents the result returned to the LLM.
// Parameters are omitted from the JSON response to save context tokens;
// the LLM will see full schemas via ToProviderDefs after promotion.
type ToolSearchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func (r *ToolRegistry) SearchRegex(pattern string, maxSearchResults int) ([]ToolSearchResult, error) {
	return r.searchRegex(pattern, maxSearchResults, nil)
}

// searchRegex is SearchRegex without the tools hidden reports, if not nil.
func (r *ToolRegistry) searchRegex(
	pattern string,
	maxSearchResults int,
	hidden func(name string, tool Tool) bool,
) ([]ToolSearchResult, error) {
	if maxSearchResults <= 0 {
		return nil, nil
	}

	regex, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil, fmt.Errorf("failed to compile regex pattern %q: %w", pattern, err)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var results []ToolSearchResult

	// Iterate in sorted order for deterministic results across calls.
	for _, name := range r.sortedToolNames() {
		entry := r.tools[name]
		// Search only among the hidden tools (Core tools are already visible)
		if !entry.IsCore {
			if hidden != nil && hidden(name, entry.Tool) {
				continue
			}
			// Directly call interface methods! No reflection/unmarshalling needed.
			desc := entry.Tool.Description()

			if regex.MatchString(name) || regex.MatchString(desc) {
				results = append(results, ToolSearchResult{
					Name:        name,
					Description: desc,
				})
				if len(results) >= maxSearchResults {
					break // Stop searching once we hit the max! Saves CPU.
				}
			}
		}
	}

	return results, nil
}

// formatDiscoveryResponse unlocks the found tools for the session that
// searched -- other sessions of the agent keep their own tool lists -- and
// describes them to the model.
func formatDiscoveryResponse(registry *ToolRegistry, sessionKey string, results []ToolSearchResult, ttl int) *ToolResult {
	if len(results) == 0 {
		return SilentResult("No tools found matching the query.")
	}

	names := make([]string, len(results))
	for i, r := range results {
		names[i] = r.Name
	}
	registry.PromoteToolsForSession(sessionKey, names, ttl)
	logger.InfoCF("discovery", "Promoted tools", map[string]any{"tools": names, "ttl": ttl})

	b, err := json.Marshal(results)
	if err != nil {
		return ErrorResult("Failed to format search results: " + err.Error())
	}

	msg := fmt.Sprintf(
		"Found %d tools:\n%s\n\nSUCCESS: These tools have been temporarily UNLOCKED as native tools! In your next response, you can call them directly just like any normal tool",
		len(results),
		string(b),
	)

	return SilentResult(msg)
}

// Lightweight internal type used as corpus document for BM25.
type searchDoc struct {
	Name        string
	Description string
}

// bm25CachedEngine wraps a BM25Engine with its corpus snapshot.
type bm25CachedEngine struct {
	engine *utils.BM25Engine[searchDoc]
	// size is the number of tools in the corpus.
	size int
}

// snapshotToSearchDocs converts a HiddenToolSnapshot to BM25 searchDoc slice.
func snapshotToSearchDocs(snap HiddenToolSnapshot) []searchDoc {
	docs := make([]searchDoc, len(snap.Docs))
	for i, d := range snap.Docs {
		docs[i] = searchDoc{Name: d.Name, Description: d.Description}
	}
	return docs
}

// buildBM25Engine creates a BM25Engine from a slice of searchDocs.
func buildBM25Engine(docs []searchDoc) *utils.BM25Engine[searchDoc] {
	return utils.NewBM25Engine(
		docs,
		func(doc searchDoc) string {
			return doc.Name + " " + doc.Description
		},
	)
}

// getOrBuildEngine returns a cached BM25 engine, rebuilding it only when
// the registry version has changed (new tools registered).
func (t *BM25SearchTool) getOrBuildEngine() *bm25CachedEngine {
	// Fast path: one atomic load gives a consistent engine and version.
	if cached := t.cache.Load(); cached != nil && cached.version == t.registry.Version() {
		return cached.engine
	}

	t.cacheMu.Lock()
	defer t.cacheMu.Unlock()

	// Snapshot + version are read under a single registry RLock,
	// guaranteeing consistency (no TOCTOU).
	snap := t.registry.SnapshotHiddenTools()

	// Re-check: another goroutine may have rebuilt while we waited for cacheMu.
	if cached := t.cache.Load(); cached != nil && cached.version == snap.Version {
		return cached.engine
	}

	docs := snapshotToSearchDocs(snap)
	if len(docs) == 0 {
		t.cache.Store(&bm25Cache{version: snap.Version})
		return nil
	}

	engine := &bm25CachedEngine{engine: buildBM25Engine(docs), size: len(docs)}
	t.cache.Store(&bm25Cache{engine: engine, version: snap.Version})
	logger.DebugCF("discovery", "BM25 engine rebuilt", map[string]any{"docs": len(docs), "version": snap.Version})
	return engine
}

func isToolDiscoveryToolName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case BM25SearchToolName, RegexSearchToolName:
		return true
	default:
		return false
	}
}

// SearchBM25 ranks hidden tools against query using BM25 via utils.BM25Engine.
// This non-cached variant rebuilds the engine on every call. Used by tests
// and any code that doesn't hold a BM25SearchTool instance.
func (r *ToolRegistry) SearchBM25(query string, maxSearchResults int) []ToolSearchResult {
	snap := r.SnapshotHiddenTools()
	docs := snapshotToSearchDocs(snap)
	if len(docs) == 0 {
		return nil
	}

	ranked := buildBM25Engine(docs).Search(query, maxSearchResults)
	if len(ranked) == 0 {
		return nil
	}

	out := make([]ToolSearchResult, len(ranked))
	for i, r := range ranked {
		out[i] = ToolSearchResult{
			Name:        r.Document.Name,
			Description: r.Document.Description,
		}
	}
	return out
}
