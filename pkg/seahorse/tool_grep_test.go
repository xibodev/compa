package seahorse

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/xibodev/compa/v2/pkg/tools"
)

func TestGrepSearchSummaries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	conv, _ := s.GetOrCreateConversation(ctx, "test:grep-tool")

	s.CreateSummary(ctx, CreateSummaryInput{
		ConversationID: conv.ConversationID,
		Kind:           SummaryKindLeaf,
		Depth:          0,
		Content:        "database connection pool configuration",
		TokenCount:     50,
	})

	re := &RetrievalEngine{store: s}
	results, err := re.Grep(ctx, GrepInput{
		Pattern: "database",
	})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(results.Summaries) == 0 {
		t.Error("expected at least 1 summary result")
	}
}

func TestGrepSearchMessages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	conv, _ := s.GetOrCreateConversation(ctx, "test:grep-msg")

	s.AddMessage(ctx, conv.ConversationID, "user", "find this message about testing", 5)
	s.AddMessage(ctx, conv.ConversationID, "user", "unrelated content", 3)

	re := &RetrievalEngine{store: s}
	results, err := re.Grep(ctx, GrepInput{
		Pattern: "testing",
	})
	if err != nil {
		t.Fatalf("Grep messages: %v", err)
	}
	if len(results.Messages) == 0 {
		t.Error("expected at least 1 message result")
	}
}

func TestGrepMissingPattern(t *testing.T) {
	s := openTestStore(t)
	re := &RetrievalEngine{store: s}
	_, err := re.Grep(context.Background(), GrepInput{})
	if err == nil {
		t.Error("expected error for missing pattern")
	}
}

func TestGrepToolSupportsAllConversations(t *testing.T) {
	s := openTestStore(t)
	tool := NewGrepTool(&RetrievalEngine{store: s})
	params := tool.Parameters()
	props := params["properties"].(map[string]any)

	// GrepTool should accept all_conversations parameter
	if _, ok := props["all_conversations"]; !ok {
		t.Error("Parameters missing 'all_conversations' field")
	}
}

// short_grep searches only the calling turn's conversation by default (EV-08).
func TestGrepToolScopedToCurrentConversation(t *testing.T) {
	s := openTestStore(t)
	bg := context.Background()
	mine, _ := s.GetOrCreateConversation(bg, "test:grep-mine")
	other, _ := s.GetOrCreateConversation(bg, "test:grep-other")
	s.AddMessage(bg, mine.ConversationID, "user", "deploy the payroll service", 5)
	s.AddMessage(bg, other.ConversationID, "user", "payroll password is hunter2", 5)

	tool := NewGrepTool(&RetrievalEngine{store: s})
	count := func(t *testing.T, ctx context.Context, args map[string]any) int {
		t.Helper()
		res := tool.Execute(ctx, args)
		if res.IsError {
			t.Fatalf("Execute: %s", res.ForLLM)
		}
		var out struct {
			Messages []GrepMessageResult `json:"messages"`
		}
		if err := json.Unmarshal([]byte(res.ForLLM), &out); err != nil {
			t.Fatalf("parse: %v", err)
		}
		for _, m := range out.Messages {
			if m.ConversationID != mine.ConversationID && args["all_conversations"] != true {
				t.Fatalf("result from conversation %d leaked: %q", m.ConversationID, m.Snippet)
			}
		}
		return len(out.Messages)
	}

	ctx := tools.WithToolSessionContext(bg, "main", "test:grep-mine", nil)
	if n := count(t, ctx, map[string]any{"pattern": "payroll"}); n != 1 {
		t.Fatalf("scoped search found %d messages, want 1", n)
	}
	if n := count(t, ctx, map[string]any{"pattern": "%payroll%"}); n != 1 {
		t.Fatalf("scoped LIKE search found %d messages, want 1", n)
	}
	if n := count(t, tools.WithToolSessionContext(bg, "main", "test:none", nil),
		map[string]any{"pattern": "payroll"}); n != 0 {
		t.Fatalf("session without history found %d messages, want 0", n)
	}
	if n := count(t, ctx, map[string]any{"pattern": "payroll", "all_conversations": true}); n != 2 {
		t.Fatalf("all_conversations found %d messages, want 2", n)
	}
}

// LIKE mode treats _ literally (EV-20).
func TestGrepLikeEscapesUnderscore(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	conv, _ := s.GetOrCreateConversation(ctx, "test:grep-like")
	s.AddMessage(ctx, conv.ConversationID, "user", "set max_tokens to 10", 5)
	s.AddMessage(ctx, conv.ConversationID, "user", "set maxXtokens to 10", 5)

	re := &RetrievalEngine{store: s}
	res, err := re.Grep(ctx, GrepInput{Pattern: "%max_tokens%", Scope: "message", ConversationID: conv.ConversationID})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(res.Messages) != 1 || !strings.Contains(res.Messages[0].Snippet, "max_tokens") {
		t.Fatalf("messages = %+v, want only the literal max_tokens match", res.Messages)
	}
}

// since/before in another zone are compared as UTC, like stored times (EV-20).
func TestGrepSinceUsesUTC(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	conv, _ := s.GetOrCreateConversation(ctx, "test:grep-utc")
	created := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	if _, err := s.AddMessageWithReasoning(ctx, conv.ConversationID, "user", "timezone check", "", "", 5, created); err != nil {
		t.Fatalf("AddMessage: %v", err)
	}

	// 13:00 at UTC+2 is 11:00 UTC, an hour before the message.
	zone := time.FixedZone("UTC+2", 2*3600)
	since := time.Date(2026, 1, 10, 13, 0, 0, 0, zone)
	re := &RetrievalEngine{store: s}
	res, err := re.Grep(ctx, GrepInput{Pattern: "timezone", Scope: "message", Since: &since})
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("since in UTC+2 found %d messages, want 1", len(res.Messages))
	}
}
