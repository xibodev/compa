package agent_test

import (
	"context"
	"testing"

	"github.com/xibodev/compa/pkg/agent"
	toolshared "github.com/xibodev/compa/pkg/tools/shared"
)

// echoTool is a minimal tool implementation for testing the public API.
type echoTool struct{}

func (t *echoTool) Name() string        { return "echo_test" }
func (t *echoTool) Description() string { return "Echoes the input back" }
func (t *echoTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"message": map[string]any{
				"type":        "string",
				"description": "Message to echo",
			},
		},
		"required": []string{"message"},
	}
}
func (t *echoTool) Execute(ctx context.Context, args map[string]any) *toolshared.ToolResult {
	msg, _ := args["message"].(string)
	return &toolshared.ToolResult{
		ForLLM: "echo: " + msg,
	}
}

// testToolProvider implements agent.ToolProvider for testing.
type testToolProvider struct{}

func (p *testToolProvider) RegisterTools(workspace string, register func(agent.Tool)) ([]string, func(string) (string, string, []string)) {
	register(&echoTool{})
	return []string{"echo_test"}, nil
}

// TestPublicAPIImportability verifies that the public API of the Compa runtime
// can be imported and used by an external consumer without hitting internal
// package import restrictions.
func TestPublicAPIImportability(t *testing.T) {
	// Verify Tool interface is satisfied
	var tool agent.Tool = &echoTool{}
	if tool.Name() != "echo_test" {
		t.Errorf("expected name 'echo_test', got %q", tool.Name())
	}

	// Verify ToolProvider interface is satisfied
	var provider agent.ToolProvider = &testToolProvider{}
	summaries, _ := provider.RegisterTools("/tmp/test", func(tool agent.Tool) {})
	if len(summaries) != 1 || summaries[0] != "echo_test" {
		t.Errorf("expected summaries [echo_test], got %v", summaries)
	}

	t.Logf("Public API importability verified: Tool, ToolProvider interfaces work")
}
