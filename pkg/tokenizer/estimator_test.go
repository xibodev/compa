package tokenizer

import (
	"strings"
	"testing"

	"github.com/xibodev/compa/pkg/providers"
)

func TestEstimateTextTokensCountsCJKRunesAsTokens(t *testing.T) {
	latin := strings.Repeat("a", 100)
	if got := EstimateTextTokens(latin); got != 40 {
		t.Fatalf("latin estimate = %d, want 40 (2.5 characters per token)", got)
	}
	cjk := strings.Repeat("你", 100)
	if got := EstimateTextTokens(cjk); got != 100 {
		t.Fatalf("CJK estimate = %d, want 100 (one token per rune)", got)
	}
	if got := EstimateTextTokens(strings.Repeat("한", 10) + strings.Repeat("b", 10)); got != 14 {
		t.Fatalf("mixed estimate = %d, want 14", got)
	}
}

func TestEstimateMessageTokensKeepsLatinEstimate(t *testing.T) {
	// 11 characters of content plus 12 of per-message overhead.
	msg := providers.Message{Role: "user", Content: "hello world"}
	if got := EstimateMessageTokens(msg); got != 9 {
		t.Fatalf("EstimateMessageTokens = %d, want 9", got)
	}
}

func TestEstimateMessageTokensDoesNotUnderestimateCJK(t *testing.T) {
	content := strings.Repeat("这是一个测试", 50) // 300 runes
	msg := providers.Message{Role: "user", Content: content}
	if got := EstimateMessageTokens(msg); got < 300 {
		t.Fatalf("EstimateMessageTokens = %d, want at least one token per CJK rune (300)", got)
	}
}

func TestEstimateToolDefsTokensCountsCJKDescriptions(t *testing.T) {
	latin := []providers.ToolDefinition{{Function: providers.ToolFunctionDefinition{
		Name: "t", Description: strings.Repeat("d", 100),
	}}}
	cjk := []providers.ToolDefinition{{Function: providers.ToolFunctionDefinition{
		Name: "t", Description: strings.Repeat("描", 100),
	}}}
	if EstimateToolDefsTokens(cjk) <= EstimateToolDefsTokens(latin) {
		t.Fatalf("CJK description estimate %d should exceed latin %d",
			EstimateToolDefsTokens(cjk), EstimateToolDefsTokens(latin))
	}
}
