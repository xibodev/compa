package channels

import (
	"strings"
	"testing"
	"time"
)

// checkSplit runs SplitMessage under a watchdog and checks that it ends,
// that every chunk fits maxLen, and that every chunk consumed input.
func checkSplit(t *testing.T, content string, maxLen int) []string {
	t.Helper()
	done := make(chan []string, 1)
	go func() { done <- SplitMessage(content, maxLen) }()
	var chunks []string
	select {
	case chunks = <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("SplitMessage(%d runes, %d) did not finish", len([]rune(content)), maxLen)
	}
	for i, chunk := range chunks {
		if n := len([]rune(chunk)); n > maxLen {
			t.Fatalf("chunk %d has %d runes, more than maxLen %d: %q", i, n, maxLen, chunk)
		}
	}
	if total := len([]rune(content)); len(chunks) > total+1 {
		t.Fatalf("%d chunks for %d runes", len(chunks), total)
	}
	return chunks
}

func TestSplitMessageTerminatesWithLongFenceHeaders(t *testing.T) {
	tests := []struct {
		name    string
		content string
		maxLen  int
	}{
		{
			name:    "header near maxLen, then a blank line",
			content: "```" + strings.Repeat("x", 97) + "\n\n" + strings.Repeat("z", 1000),
			maxLen:  300,
		},
		{
			name:    "header just under maxLen",
			content: "```" + strings.Repeat("x", 290) + "\n" + strings.Repeat("y\n", 600),
			maxLen:  300,
		},
		{
			name:    "header longer than maxLen, no newline",
			content: "```" + strings.Repeat("x", 3000),
			maxLen:  300,
		},
		{
			name:    "header longer than maxLen, then code",
			content: "intro\n```" + strings.Repeat("h", 700) + "\n" + strings.Repeat("code line\n", 300),
			maxLen:  200,
		},
		{
			name:    "many unclosed fences",
			content: strings.Repeat("```lang\nsome code\n", 400),
			maxLen:  64,
		},
		{
			name:    "fence with tiny maxLen",
			content: "```go\n" + strings.Repeat("fmt.Println()\n", 20),
			maxLen:  5,
		},
		{
			name:    "fence at the smallest fence-aware maxLen",
			content: "```" + strings.Repeat("y", 200) + "\n" + strings.Repeat("q", 200),
			maxLen:  minFenceSplitLen,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			chunks := checkSplit(t, tt.content, tt.maxLen)
			if len(chunks) == 0 {
				t.Fatal("no chunks")
			}
		})
	}
}

func TestSplitMessageReopensShortFenceHeader(t *testing.T) {
	content := "```python\n" + strings.Repeat("print('hello world')\n", 40) + "```"
	chunks := checkSplit(t, content, 200)
	if len(chunks) < 2 {
		t.Fatalf("expected a split, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks[1:] {
		if !strings.HasPrefix(chunk, "```python\n") {
			t.Fatalf("chunk %d does not reopen the block: %q", i+1, chunk)
		}
	}
}

func TestSplitMessageLongHeaderReopensBareFence(t *testing.T) {
	header := "```" + strings.Repeat("x", 120)
	content := header + "\n" + strings.Repeat("line of code\n", 60)
	chunks := checkSplit(t, content, 200)
	if len(chunks) < 2 {
		t.Fatalf("expected a split, got %d chunk(s)", len(chunks))
	}
	if strings.Contains(chunks[1], header) {
		t.Fatal("the long header was repeated in the next chunk")
	}
	if !strings.HasPrefix(chunks[1], "```\n") {
		t.Fatalf("the next chunk does not reopen the block: %q", chunks[1])
	}
}

func FuzzSplitMessage(f *testing.F) {
	f.Add("```"+strings.Repeat("x", 97)+"\n\n"+strings.Repeat("z", 1000), 300)
	f.Add("```go\nfunc main() {}\n```\n"+strings.Repeat("word ", 300), 100)
	f.Add(strings.Repeat("```", 50), 20)
	f.Add(strings.Repeat("\u4e16", 500), 7)
	f.Add("```"+strings.Repeat("h", 400), 16)
	f.Fuzz(func(t *testing.T, content string, maxLen int) {
		if maxLen < 0 {
			maxLen = -(maxLen + 1)
		}
		maxLen = maxLen%2000 + 1
		checkSplit(t, content, maxLen)
	})
}
