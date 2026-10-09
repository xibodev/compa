package integrationtools

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/xibodev/compa/v4/pkg/approval"
	"github.com/xibodev/compa/v4/pkg/config"
	runtimeevents "github.com/xibodev/compa/v4/pkg/events"
	mcpclient "github.com/xibodev/compa/v4/pkg/mcp"
	"github.com/xibodev/compa/v4/pkg/media"
	toolshared "github.com/xibodev/compa/v4/pkg/tools/shared"
)

// MockMCPManager is a mock implementation of MCPManager interface for testing
type MockMCPManager struct {
	callToolFunc func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error)
}

func (m *MockMCPManager) CallTool(
	ctx context.Context,
	serverName, toolName string,
	arguments map[string]any,
) (*mcp.CallToolResult, error) {
	if m.callToolFunc != nil {
		return m.callToolFunc(ctx, serverName, toolName, arguments)
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: "mock result"},
		},
		IsError: false,
	}, nil
}

func (m *MockMCPManager) CallTimeout(string) time.Duration { return mcpclient.DefaultCallTimeout }

// The tool's call timeout is its server's: the manager ends every call then.
func TestMCPToolCallTimeoutIsTheServers(t *testing.T) {
	tool := NewMCPTool(&MockMCPManager{}, "server", &mcp.Tool{Name: "tool"})
	if got := tool.CallTimeout(context.Background(), nil); got != mcpclient.DefaultCallTimeout {
		t.Fatalf("CallTimeout() = %s, want the server's %s", got, mcpclient.DefaultCallTimeout)
	}
}

// TestNewMCPTool verifies MCP tool creation
func TestNewMCPTool(t *testing.T) {
	manager := &MockMCPManager{}
	tool := &mcp.Tool{
		Name:        "test_tool",
		Description: "A test tool",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"input": map[string]any{
					"type":        "string",
					"description": "Test input",
				},
			},
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", tool)

	if mcpTool == nil {
		t.Fatal("NewMCPTool should not return nil")
	}
	// Verify tool properties we can access
	if mcpTool.Name() != "mcp_test_server_test_tool" {
		t.Errorf("Expected tool name with prefix, got '%s'", mcpTool.Name())
	}
}

// TestMCPTool_Name verifies tool name with server prefix
func TestMCPTool_Name(t *testing.T) {
	hash := func(server, tool string) string {
		h := fnv.New32a()
		_, _ = h.Write([]byte(server + "\x00" + tool))
		return fmt.Sprintf("%08x", h.Sum32())
	}
	tests := []struct {
		name       string
		serverName string
		toolName   string
		hashed     bool
		expected   string
	}{
		{
			name:       "simple name",
			serverName: "github",
			toolName:   "create_issue",
			expected:   "mcp_github_create_issue",
		},
		{
			name:       "filesystem server",
			serverName: "filesystem",
			toolName:   "read_file",
			expected:   "mcp_filesystem_read_file",
		},
		{
			name:       "remote server",
			serverName: "remote-api",
			toolName:   "fetch_data",
			expected:   "mcp_remote-api_fetch_data",
		},
		{
			name:       "case is kept",
			serverName: "GitHub",
			toolName:   "createIssue",
			expected:   "mcp_GitHub_createIssue",
		},
		{
			name:       "sanitized server",
			serverName: "my server",
			toolName:   "search",
			expected:   "mcp_my_server_search_" + hash("my server", "search"),
		},
		{
			name:       "sanitized tool",
			serverName: "docs",
			toolName:   "search.pages",
			expected:   "mcp_docs_search_pages_" + hash("docs", "search.pages"),
		},
		{
			name:       "name another tool holds",
			serverName: "a_b",
			toolName:   "c",
			hashed:     true,
			expected:   "mcp_a_b_c_" + hash("a_b", "c"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := &mcp.Tool{Name: tt.toolName}
			mcpTool := NewMCPTool(manager, tt.serverName, tool)
			if tt.hashed {
				mcpTool.UseHashedName()
			}

			result := mcpTool.Name()
			if result != tt.expected {
				t.Errorf("Expected name '%s', got '%s'", tt.expected, result)
			}
		})
	}

	// Server and tool splits that join to the same plain name stay apart
	// once the second is hashed, and a long name stays within 64 characters.
	first := NewMCPTool(&MockMCPManager{}, "a", &mcp.Tool{Name: "b_c"})
	second := NewMCPTool(&MockMCPManager{}, "a_b", &mcp.Tool{Name: "c"})
	second.UseHashedName()
	if first.Name() == second.Name() {
		t.Fatalf("colliding names were not told apart: %q", first.Name())
	}
	long := NewMCPTool(&MockMCPManager{}, strings.Repeat("s", 40), &mcp.Tool{Name: strings.Repeat("t", 40)})
	if got := long.Name(); len(got) > 64 || !strings.HasSuffix(got, hash(strings.Repeat("s", 40), strings.Repeat("t", 40))) {
		t.Fatalf("long name = %q", got)
	}
}

func TestMCPTool_PromptMetadata(t *testing.T) {
	manager := &MockMCPManager{}
	tool := NewMCPTool(manager, "GitHub Server", &mcp.Tool{Name: "create_issue"})

	metadata := tool.PromptMetadata()
	if metadata.Layer != toolshared.ToolPromptLayerCapability {
		t.Fatalf("metadata.Layer = %q, want %q", metadata.Layer, toolshared.ToolPromptLayerCapability)
	}
	if metadata.Slot != toolshared.ToolPromptSlotMCP {
		t.Fatalf("metadata.Slot = %q, want %q", metadata.Slot, toolshared.ToolPromptSlotMCP)
	}
	if metadata.Source != "mcp:github_server" {
		t.Fatalf("metadata.Source = %q, want mcp:github_server", metadata.Source)
	}
}

// TestMCPTool_Description verifies tool description generation
func TestMCPTool_Description(t *testing.T) {
	tests := []struct {
		name            string
		serverName      string
		toolDescription string
		expectContains  []string
	}{
		{
			name:            "with description",
			serverName:      "github",
			toolDescription: "Create a GitHub issue",
			expectContains:  []string{"[MCP:github]", "Create a GitHub issue"},
		},
		{
			name:            "empty description",
			serverName:      "filesystem",
			toolDescription: "",
			expectContains:  []string{"[MCP:filesystem]", "MCP tool from filesystem server"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := &mcp.Tool{
				Name:        "test_tool",
				Description: tt.toolDescription,
			}
			mcpTool := NewMCPTool(manager, tt.serverName, tool)

			result := mcpTool.Description()

			for _, expected := range tt.expectContains {
				if !strings.Contains(result, expected) {
					t.Errorf("Description should contain '%s', got: %s", expected, result)
				}
			}
		})
	}
}

// TestMCPTool_Parameters verifies parameter schema conversion
func TestMCPTool_Parameters(t *testing.T) {
	tests := []struct {
		name           string
		inputSchema    any
		expectType     string
		checkProperty  string
		expectProperty bool
	}{
		{
			name: "map schema",
			inputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query",
					},
				},
				"required": []string{"query"},
			},
			expectType:     "object",
			checkProperty:  "query",
			expectProperty: true,
		},
		{
			name:           "nil schema",
			inputSchema:    nil,
			expectType:     "object",
			expectProperty: false,
		},
		{
			name: "json.RawMessage schema",
			inputSchema: []byte(`{
				"type": "object",
				"properties": {
					"repo": {
						"type": "string",
						"description": "Repository name"
					},
					"stars": {
						"type": "integer",
						"description": "Minimum stars"
					}
				},
				"required": ["repo"]
			}`),
			expectType:     "object",
			checkProperty:  "repo",
			expectProperty: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := &MockMCPManager{}
			tool := &mcp.Tool{
				Name:        "test_tool",
				InputSchema: tt.inputSchema,
			}
			mcpTool := NewMCPTool(manager, "test_server", tool)

			params := mcpTool.Parameters()

			if params == nil {
				t.Fatal("Parameters should not be nil")
			}

			if params["type"] != tt.expectType {
				t.Errorf("Expected type '%s', got '%v'", tt.expectType, params["type"])
			}

			// Check if property exists when expected
			if tt.checkProperty != "" {
				properties, ok := params["properties"].(map[string]any)
				if !ok && tt.expectProperty {
					t.Errorf("Expected properties to be a map")
					return
				}
				if ok {
					_, hasProperty := properties[tt.checkProperty]
					if hasProperty != tt.expectProperty {
						t.Errorf("Expected property '%s' existence: %v, got: %v",
							tt.checkProperty, tt.expectProperty, hasProperty)
					}
				}
			}
		})
	}
}

// TestMCPTool_Execute_Success tests successful tool execution
func TestMCPTool_Execute_Success(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			// Verify correct parameters passed
			if serverName != "github" {
				t.Errorf("Expected serverName 'github', got '%s'", serverName)
			}
			if toolName != "search_repos" {
				t.Errorf("Expected toolName 'search_repos', got '%s'", toolName)
			}

			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "Found 3 repositories"},
				},
				IsError: false,
			}, nil
		},
	}

	tool := &mcp.Tool{
		Name:        "search_repos",
		Description: "Search GitHub repositories",
	}
	mcpTool := NewMCPTool(manager, "github", tool)

	ctx := context.Background()
	args := map[string]any{
		"query": "golang mcp",
	}

	result := mcpTool.Execute(ctx, args)

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if result.IsError {
		t.Errorf("Expected no error, got error: %s", result.ForLLM)
	}
	if result.ForLLM != "Found 3 repositories" {
		t.Errorf("Expected 'Found 3 repositories', got '%s'", result.ForLLM)
	}
}

func TestMCPTool_Execute_PublishesRuntimeEvents(t *testing.T) {
	eventBus := runtimeevents.NewBus()
	defer func() {
		if err := eventBus.Close(); err != nil {
			t.Errorf("event bus close failed: %v", err)
		}
	}()

	_, eventsCh, err := eventBus.Channel().OfKind(
		runtimeevents.KindMCPToolCallStart,
		runtimeevents.KindMCPToolCallEnd,
	).SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "mcp-tool-events", Buffer: 2})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	manager := &MockMCPManager{}
	mcpTool := NewMCPTool(manager, "github", &mcp.Tool{Name: "search_repos"})
	mcpTool.SetEventPublisher(eventBus)

	ctx := toolshared.WithToolContext(context.Background(), "telegram", "chat-1")
	ctx = toolshared.WithToolMessageContext(ctx, "msg-1", "")
	ctx = toolshared.WithToolSessionContext(ctx, "main", "session-1", nil)
	result := mcpTool.Execute(ctx, map[string]any{"query": "compa"})
	if result == nil || result.IsError {
		t.Fatalf("Execute result = %+v", result)
	}

	started := receiveMCPToolRuntimeEvent(t, eventsCh)
	if started.Kind != runtimeevents.KindMCPToolCallStart ||
		started.Scope.AgentID != "main" ||
		started.Scope.SessionKey != "session-1" ||
		started.Scope.Channel != "telegram" ||
		started.Scope.ChatID != "chat-1" ||
		started.Scope.MessageID != "msg-1" {
		t.Fatalf("started event = %+v", started)
	}

	ended := receiveMCPToolRuntimeEvent(t, eventsCh)
	if ended.Kind != runtimeevents.KindMCPToolCallEnd || ended.Severity != runtimeevents.SeverityInfo {
		t.Fatalf("ended event = %+v", ended)
	}
	payload, ok := ended.Payload.(MCPToolCallPayload)
	if !ok {
		t.Fatalf("ended payload = %T, want MCPToolCallPayload", ended.Payload)
	}
	if payload.Server != "github" || payload.Tool != "search_repos" || payload.IsError {
		t.Fatalf("ended payload = %+v", payload)
	}
	if ended.Attrs["server"] != "github" ||
		ended.Attrs["tool"] != "search_repos" ||
		ended.Attrs["duration_ms"] == nil {
		t.Fatalf("ended attrs = %#v", ended.Attrs)
	}
}

func receiveMCPToolRuntimeEvent(t *testing.T, ch <-chan runtimeevents.Event) runtimeevents.Event {
	t.Helper()

	select {
	case evt, ok := <-ch:
		if !ok {
			t.Fatal("runtime event channel closed before expected event")
		}
		return evt
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for runtime event")
		return runtimeevents.Event{}
	}
}

// The progress a server reports for a call becomes progress events, at most
// one per second per call.
func TestMCPTool_Execute_PublishesProgress(t *testing.T) {
	release := make(chan struct{})
	server := mcp.NewServer(&mcp.Implementation{Name: "slow", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "build", Description: "reports progress"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ map[string]any) (*mcp.CallToolResult, any, error) {
			for i := 1; i <= 5; i++ {
				if err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: req.Params.GetProgressToken(),
					Progress:      float64(i),
					Total:         5,
					Message:       fmt.Sprintf("step %d", i),
				}); err != nil {
					return nil, nil, err
				}
			}
			select {
			case <-release:
			case <-time.After(10 * time.Second):
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "built"}}}, nil, nil
		})
	ts := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer ts.Close()

	manager := mcpclient.NewManager()
	defer manager.Close()
	if err := manager.ConnectServer(context.Background(), "ci",
		config.MCPServerConfig{Enabled: true, Type: "http", URL: ts.URL}); err != nil {
		t.Fatalf("ConnectServer() error = %v", err)
	}

	eventBus := runtimeevents.NewBus()
	defer eventBus.Close()
	_, eventsCh, err := eventBus.Channel().OfKind(runtimeevents.KindMCPToolCallProgress).
		SubscribeChan(t.Context(), runtimeevents.SubscribeOptions{Name: "mcp-progress", Buffer: 16})
	if err != nil {
		t.Fatalf("SubscribeChan failed: %v", err)
	}

	mcpTool := NewMCPTool(manager, "ci", &mcp.Tool{Name: "build"})
	mcpTool.SetEventPublisher(eventBus)
	done := make(chan *ToolResult, 1)
	go func() {
		done <- mcpTool.Execute(toolshared.WithToolContext(context.Background(), "telegram", "chat-1"), nil)
	}()

	progress := receiveMCPToolRuntimeEvent(t, eventsCh)
	close(release)
	payload, ok := progress.Payload.(MCPToolCallProgressPayload)
	if !ok {
		t.Fatalf("progress payload = %T, want MCPToolCallProgressPayload", progress.Payload)
	}
	if payload != (MCPToolCallProgressPayload{Server: "ci", Tool: "build", Progress: 1, Total: 5, Message: "step 1"}) {
		t.Fatalf("progress payload = %+v", payload)
	}
	if progress.Scope.ChatID != "chat-1" || progress.Attrs["message"] != "step 1" {
		t.Fatalf("progress event = %+v", progress)
	}
	if result := <-done; result.IsError || result.ForLLM != "built" {
		t.Fatalf("Execute result = %+v", result)
	}
	select {
	case evt := <-eventsCh:
		t.Fatalf("a second progress event within a second: %+v", evt.Payload)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestMCPTool_Execute_ManagerError tests execution when manager returns error
func TestMCPTool_Execute_ManagerError(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return nil, fmt.Errorf("connection failed")
		},
	}

	tool := &mcp.Tool{Name: "test_tool"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if !strings.Contains(result.ForLLM, "MCP tool execution failed") {
		t.Errorf("Error message should mention execution failure, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "connection failed") {
		t.Errorf("Error message should include original error, got: %s", result.ForLLM)
	}
}

// TestMCPTool_Execute_ServerError tests execution when server returns error
func TestMCPTool_Execute_ServerError(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "Invalid API key"},
				},
				IsError: true,
			}, nil
		},
	}

	tool := &mcp.Tool{Name: "test_tool"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result == nil {
		t.Fatal("Result should not be nil")
	}
	if !result.IsError {
		t.Error("Expected IsError to be true")
	}
	if !strings.Contains(result.ForLLM, "MCP tool returned error") {
		t.Errorf("Error message should mention server error, got: %s", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "Invalid API key") {
		t.Errorf("Error message should include server message, got: %s", result.ForLLM)
	}
}

// TestMCPTool_Execute_MultipleContent tests execution with multiple content items
func TestMCPTool_Execute_MultipleContent(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: "First line"},
					&mcp.TextContent{Text: "Second line"},
					&mcp.TextContent{Text: "Third line"},
				},
				IsError: false,
			}, nil
		},
	}

	tool := &mcp.Tool{Name: "multi_output"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	ctx := context.Background()
	result := mcpTool.Execute(ctx, map[string]any{})

	if result.IsError {
		t.Errorf("Expected no error, got: %s", result.ForLLM)
	}

	expected := "First line\nSecond line\nThird line"
	if result.ForLLM != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result.ForLLM)
	}
}

// TestExtractContentText_TextContent tests text content extraction
func TestExtractContentText_TextContent(t *testing.T) {
	content := []mcp.Content{
		&mcp.TextContent{Text: "Hello World"},
		&mcp.TextContent{Text: "Second message"},
	}

	result := extractContentText(content)
	expected := "Hello World\nSecond message"

	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

// TestExtractContentText_ImageContent tests image content extraction
func TestExtractContentText_ImageContent(t *testing.T) {
	content := []mcp.Content{
		&mcp.ImageContent{
			Data:     []byte("base64data"),
			MIMEType: "image/png",
		},
	}

	result := extractContentText(content)

	if !strings.Contains(result, "[Image:") {
		t.Errorf("Expected image indicator, got: %s", result)
	}
	if !strings.Contains(result, "image/png") {
		t.Errorf("Expected MIME type in output, got: %s", result)
	}
}

// TestExtractContentText_MixedContent tests mixed content types
func TestExtractContentText_MixedContent(t *testing.T) {
	content := []mcp.Content{
		&mcp.TextContent{Text: "Description"},
		&mcp.ImageContent{
			Data:     []byte("data"),
			MIMEType: "image/jpeg",
		},
		&mcp.TextContent{Text: "More text"},
	}

	result := extractContentText(content)

	if !strings.Contains(result, "Description") {
		t.Errorf("Should contain text content, got: %s", result)
	}
	if !strings.Contains(result, "[Image:") {
		t.Errorf("Should contain image indicator, got: %s", result)
	}
	if !strings.Contains(result, "More text") {
		t.Errorf("Should contain second text, got: %s", result)
	}
}

// TestExtractContentText_EmptyContent tests empty content array
func TestExtractContentText_EmptyContent(t *testing.T) {
	content := []mcp.Content{}

	result := extractContentText(content)

	if result != "" {
		t.Errorf("Expected empty string for empty content, got: %s", result)
	}
}

// TestMCPTool_InterfaceCompliance verifies MCPTool implements Tool interface
func TestMCPTool_InterfaceCompliance(t *testing.T) {
	manager := &MockMCPManager{}
	tool := &mcp.Tool{Name: "test"}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	// Verify it implements Tool interface
	var _ Tool = mcpTool
}

// TestMCPTool_Parameters_MapSchema tests schema that's already a map
func TestMCPTool_Parameters_MapSchema(t *testing.T) {
	manager := &MockMCPManager{}
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The name parameter",
			},
		},
		"required": []string{"name"},
	}

	tool := &mcp.Tool{
		Name:        "test_tool",
		InputSchema: schema,
	}
	mcpTool := NewMCPTool(manager, "test_server", tool)

	params := mcpTool.Parameters()

	// Should return the schema as-is when it's already a map
	if params["type"] != "object" {
		t.Errorf("Expected type 'object', got '%v'", params["type"])
	}

	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Error("Properties should be a map")
	}

	nameParam, ok := props["name"].(map[string]any)
	if !ok {
		t.Error("Name parameter should exist")
	}

	if nameParam["type"] != "string" {
		t.Errorf("Name type should be 'string', got '%v'", nameParam["type"])
	}
}

func TestMCPTool_Execute_ImageContentStoredAsMedia(t *testing.T) {
	store := media.NewFileMediaStore()
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.ImageContent{
						Data:     []byte("fake-image-bytes"),
						MIMEType: "image/png",
					},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "screenshoto", &mcp.Tool{Name: "take_screenshot"})
	mcpTool.SetMediaStore(store)

	result := mcpTool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), nil)

	if result.IsError {
		t.Fatalf("expected success, got %q", result.ForLLM)
	}
	if len(result.Media) != 1 {
		t.Fatalf("expected 1 media ref, got %d", len(result.Media))
	}
	if result.ResponseHandled {
		t.Fatal("expected MCP image artifact not to mark response as handled")
	}
	if !strings.Contains(result.ForLLM, "stored as a local media artifact") {
		t.Fatalf("expected local media artifact note, got %q", result.ForLLM)
	}

	path, meta, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatalf("expected stored media ref to resolve: %v", err)
	}
	if meta.ContentType != "image/png" {
		t.Fatalf("expected image/png content type, got %q", meta.ContentType)
	}
	if filepath.Ext(path) != ".png" {
		t.Fatalf("expected png temp file, got %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected stored media file to be readable: %v", err)
	}
	if string(data) != "fake-image-bytes" {
		t.Fatalf("expected stored media bytes to match input, got %q", string(data))
	}
}

func TestMCPTool_Execute_EmbeddedResourceBlobStoredAsMedia(t *testing.T) {
	store := media.NewFileMediaStore()
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.EmbeddedResource{
						Resource: &mcp.ResourceContents{
							URI:      "file:///tmp/report.png",
							MIMEType: "image/png",
							Blob:     []byte("blob-bytes"),
						},
					},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "grafana", &mcp.Tool{Name: "get_dashboard_image"})
	mcpTool.SetMediaStore(store)

	result := mcpTool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), nil)

	if len(result.Media) != 1 {
		t.Fatalf("expected embedded resource blob to be stored as media, got %d refs", len(result.Media))
	}
	path, _, err := store.ResolveWithMeta(result.Media[0])
	if err != nil {
		t.Fatalf("expected stored media ref to resolve: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected stored media file to be readable: %v", err)
	}
	if string(data) != "blob-bytes" {
		t.Fatalf("expected stored blob bytes to match input, got %q", string(data))
	}
}

func TestMCPTool_Execute_RespectsUserAudienceForBinaryContent(t *testing.T) {
	store := media.NewFileMediaStore()
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.ImageContent{
						Data:        []byte("assistant-only"),
						MIMEType:    "image/png",
						Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}},
					},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "screenshoto", &mcp.Tool{Name: "take_screenshot"})
	mcpTool.SetMediaStore(store)

	result := mcpTool.Execute(WithToolContext(context.Background(), "telegram", "chat-42"), nil)

	if len(result.Media) != 0 {
		t.Fatalf("expected no media ref for non-user audience, got %d", len(result.Media))
	}
	if !strings.Contains(result.ForLLM, "non-user audience") {
		t.Fatalf("expected audience note, got %q", result.ForLLM)
	}
}

func TestMCPTool_Execute_LargeBase64TextIsOmittedFromContext(t *testing.T) {
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: strings.Repeat("QUJD", 400)},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})

	result := mcpTool.Execute(context.Background(), nil)

	if result.ForLLM != largeBase64OmittedMessage {
		t.Fatalf("expected sanitized large base64 note, got %q", result.ForLLM)
	}
}

func TestMCPTool_Execute_LargeBase64TextArtifactPreservesRawPayload(t *testing.T) {
	workspace := t.TempDir()
	largeBase64 := strings.Repeat("QUJD", 400)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeBase64},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspace)
	mcpTool.SetMaxInlineTextRunes(32)

	result := mcpTool.Execute(context.Background(), nil)

	if !strings.Contains(result.ForLLM, "saved as a local artifact") {
		t.Fatalf("expected artifact note, got %q", result.ForLLM)
	}
	if result.ForLLM == largeBase64OmittedMessage {
		t.Fatalf("expected artifact note instead of sanitized base64 placeholder")
	}
	if len(result.ArtifactTags) != 1 {
		t.Fatalf("expected 1 artifact tag, got %d", len(result.ArtifactTags))
	}
	tag := result.ArtifactTags[0]
	const prefix = "[file:"
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, "]") {
		t.Fatalf("expected file artifact tag, got %q", tag)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), "]")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected artifact file to be readable: %v", err)
	}
	if string(data) != largeBase64 {
		t.Fatalf("expected artifact file contents to preserve raw MCP payload")
	}
}

func TestMCPTool_Execute_LargeTextStoredAsArtifact(t *testing.T) {
	workspace := t.TempDir()
	largeText := strings.Repeat("This is a large MCP text payload.\n", 800)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeText},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspace)

	result := mcpTool.Execute(context.Background(), nil)

	if strings.Contains(result.ForLLM, "This is a large MCP text payload") {
		t.Fatalf("expected large MCP text to be omitted from ForLLM, got %q", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "saved as a local artifact") {
		t.Fatalf("expected artifact note, got %q", result.ForLLM)
	}
	if len(result.ArtifactTags) != 1 {
		t.Fatalf("expected 1 artifact tag, got %d", len(result.ArtifactTags))
	}
	tag := result.ArtifactTags[0]
	const prefix = "[file:"
	if !strings.HasPrefix(tag, prefix) || !strings.HasSuffix(tag, "]") {
		t.Fatalf("expected file artifact tag, got %q", tag)
	}
	path := strings.TrimSuffix(strings.TrimPrefix(tag, prefix), "]")
	if !strings.HasPrefix(path, workspace) {
		t.Fatalf("expected artifact inside workspace, got %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected artifact file to be readable: %v", err)
	}
	if string(data) != strings.TrimSpace(largeText) {
		t.Fatalf("expected artifact file contents to match source text")
	}
}

func TestMCPTool_Execute_CustomInlineTextThreshold(t *testing.T) {
	workspace := t.TempDir()
	text := strings.Repeat("small custom threshold text\n", 20)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: text},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspace)
	mcpTool.SetMaxInlineTextRunes(32)

	result := mcpTool.Execute(context.Background(), nil)

	if len(result.ArtifactTags) != 1 {
		t.Fatalf("expected custom threshold to persist artifact, got %+v", result)
	}
	if strings.Contains(result.ForLLM, "small custom threshold text") {
		t.Fatalf("expected text to be omitted from ForLLM, got %q", result.ForLLM)
	}
}

func TestMCPTool_Execute_LargeTextArtifactFailureStillOmitsContext(t *testing.T) {
	workspaceRoot := t.TempDir()
	workspaceFile := filepath.Join(workspaceRoot, "not-a-directory")
	if err := os.WriteFile(workspaceFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("failed to create workspace file: %v", err)
	}

	largeText := strings.Repeat("This is a large MCP text payload.\n", 800)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeText},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(workspaceFile)

	result := mcpTool.Execute(context.Background(), nil)

	if strings.Contains(result.ForLLM, "This is a large MCP text payload") {
		t.Fatalf("expected large MCP text to be omitted from ForLLM, got %q", result.ForLLM)
	}
	if !strings.Contains(result.ForLLM, "artifact persistence failed") {
		t.Fatalf("expected persistence failure note, got %q", result.ForLLM)
	}
	if len(result.ArtifactTags) != 0 {
		t.Fatalf("expected no artifact tags on persistence failure, got %+v", result.ArtifactTags)
	}
}

func TestMCPTool_Execute_WhitespaceWorkspaceDisablesArtifactPersistence(t *testing.T) {
	largeText := strings.Repeat("This is a large MCP text payload.\n", 800)
	manager := &MockMCPManager{
		callToolFunc: func(ctx context.Context, serverName, toolName string, arguments map[string]any) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{
				Content: []mcp.Content{
					&mcp.TextContent{Text: largeText},
				},
			}, nil
		},
	}

	mcpTool := NewMCPTool(manager, "test_server", &mcp.Tool{Name: "dump_payload"})
	mcpTool.SetWorkspace(" \n\t ")

	result := mcpTool.Execute(context.Background(), nil)

	if len(result.ArtifactTags) != 0 {
		t.Fatalf("expected no artifact tags for whitespace workspace, got %+v", result.ArtifactTags)
	}
	if !strings.Contains(result.ForLLM, "This is a large MCP text payload") {
		t.Fatalf("expected large text to remain inline when workspace is blank, got %q", result.ForLLM)
	}
}

func TestMCPTool_CapsServerDescriptionAndSchema(t *testing.T) {
	props := map[string]any{}
	for i := range 100 {
		props[fmt.Sprintf("arg%03d", i)] = map[string]any{
			"type":        "object",
			"description": strings.Repeat("d", 500),
			"properties":  map[string]any{"nested": map[string]any{"type": "string", "enum": []any{strings.Repeat("e", 200)}}},
		}
	}
	tool := NewMCPTool(&MockMCPManager{}, "srv", &mcp.Tool{
		Name:        "big",
		Description: strings.Repeat("界", 5000),
		InputSchema: map[string]any{"type": "object", "properties": props, "required": []any{"arg000"}},
	})

	desc := tool.Description()
	if got := utf8.RuneCountInString(desc); got > maxMCPDescriptionRunes+20 {
		t.Fatalf("description has %d characters, want at most about %d", got, maxMCPDescriptionRunes)
	}
	if !strings.HasSuffix(desc, "…") || !utf8.ValidString(desc) {
		t.Fatalf("description should be cut on a character boundary with an ellipsis")
	}

	params := tool.Parameters()
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("json.Marshal() error: %v", err)
	}
	if len(encoded) > maxMCPSchemaBytes {
		t.Fatalf("schema is %d bytes, want at most %d", len(encoded), maxMCPSchemaBytes)
	}
	shallow, _ := params["properties"].(map[string]any)
	arg, _ := shallow["arg000"].(map[string]any)
	if len(shallow) != 100 || arg["type"] != "object" || arg["properties"] != nil {
		t.Fatalf("expected top-level arguments without nested definitions, got %v", arg)
	}
	if required, _ := params["required"].([]any); len(required) != 1 || required[0] != "arg000" {
		t.Fatalf("required list lost: %v", params["required"])
	}

	small := NewMCPTool(&MockMCPManager{}, "srv", &mcp.Tool{
		Name:        "small",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string", "enum": []any{"a"}}}},
	})
	q, _ := small.Parameters()["properties"].(map[string]any)["q"].(map[string]any)
	if q["enum"] == nil {
		t.Fatal("a schema within the cap must be passed through unchanged")
	}
}

func TestMCPToolApprovalInfo(t *testing.T) {
	no := false
	annotations := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &no}
	tool := NewMCPTool(&MockMCPManager{}, "GitHub Server", &mcp.Tool{
		Name:        "list_issues",
		Annotations: annotations,
		Meta:        mcp.Meta{"vendor/x": "y"},
	})

	// Not trusted: the declarations are kept for the approvers but are no
	// hints; the MCP defaults are.
	info := tool.ApprovalInfo()
	if info.Source != "mcp:GitHub Server" || info.Name != "list_issues" || info.Trusted ||
		info.Annotations != annotations || info.Meta["vendor/x"] != "y" {
		t.Fatalf("untrusted ApprovalInfo() = %+v", info)
	}
	if want := []string{approval.HintDestructive, approval.HintOpenWorld}; !slices.Equal(info.Hints, want) {
		t.Fatalf("untrusted hints = %v, want %v", info.Hints, want)
	}

	tool.SetTrusted(true)
	info = tool.ApprovalInfo()
	if want := []string{approval.HintReadOnly}; !info.Trusted || !slices.Equal(info.Hints, want) {
		t.Fatalf("trusted ApprovalInfo() = %+v, want hints %v", info, want)
	}

	// The trust decides which rule matches.
	policy := approval.Policy{Rules: []approval.Rule{
		{Source: "mcp:GitHub*", Hints: []string{approval.HintReadOnly}, Action: approval.Allow},
		{Source: "mcp:*", Action: approval.Ask},
	}}
	for _, tt := range []struct {
		trusted bool
		want    approval.Action
		index   int
	}{
		{true, approval.Allow, 0},
		{false, approval.Ask, 1},
	} {
		tool.SetTrusted(tt.trusted)
		policyTool, _ := approval.Describe(tool.Name(), tool)
		if d := policy.Decide(policyTool, approval.OriginChat); d.Action != tt.want || d.Index != tt.index {
			t.Fatalf("trusted=%v: decision = %+v, want %s by rule %d", tt.trusted, d, tt.want, tt.index)
		}
	}
}

func TestMCPToolApprovalInfoWithoutAnnotations(t *testing.T) {
	tool := NewMCPTool(&MockMCPManager{}, "srv", &mcp.Tool{Name: "do"})
	tool.SetTrusted(true)
	info := tool.ApprovalInfo()
	if info.Annotations != nil {
		t.Fatalf("Annotations = %v, want none declared", info.Annotations)
	}
	if want := []string{approval.HintDestructive, approval.HintOpenWorld}; !slices.Equal(info.Hints, want) {
		t.Fatalf("hints = %v, want the MCP defaults %v", info.Hints, want)
	}
}
