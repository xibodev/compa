package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v3/pkg/config"
)

func TestCappedBuffer_KeepsLimitAndMarksTheRest(t *testing.T) {
	buf := newCappedBuffer(8)
	for _, chunk := range []string{"abcde", "fghij", "klm"} {
		n, err := buf.Write([]byte(chunk))
		require.NoError(t, err)
		require.Equal(t, len(chunk), n, "a capped write must still report the whole chunk as written")
	}

	got := buf.String()
	require.True(t, strings.HasPrefix(got, "abcdefgh"), "got %q", got)
	require.Contains(t, got, "[output truncated: 5 more bytes]")
}

func TestCappedBuffer_DropsSplitRune(t *testing.T) {
	buf := newCappedBuffer(4)
	// "é" is two bytes; the limit falls inside the second one.
	_, _ = buf.Write([]byte("abcé!"))

	got := buf.String()
	require.True(t, utf8.ValidString(got), "truncated output must stay valid UTF-8: %q", got)
	require.True(t, strings.HasPrefix(got, "abc\n"), "got %q", got)
}

func TestTruncateExecOutput_CutsOnRuneBoundary(t *testing.T) {
	output := strings.Repeat("字", maxExecResultChars+25)

	got := truncateExecOutput(output)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, maxExecResultChars, utf8.RuneCountInString(strings.SplitN(got, "\n", 2)[0]))
	require.Contains(t, got, "(truncated, 25 more chars)")

	short := "short output"
	require.Equal(t, short, truncateExecOutput(short))
}

func TestExecTool_CommandTimeout(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Exec.TimeoutSeconds = 60
	configured, err := NewExecToolWithConfig("", false, cfg)
	require.NoError(t, err)

	unconfigured, err := NewExecTool("", false)
	require.NoError(t, err)

	cli := WithToolContext(context.Background(), "cli", "direct")
	chat := WithToolContext(context.Background(), "telegram", "chat-1")

	tests := []struct {
		name string
		tool *ExecTool
		ctx  context.Context
		arg  any
		want time.Duration
	}{
		{"configured, no argument", configured, chat, nil, 60 * time.Second},
		{"configured, zero means the configured timeout", configured, chat, float64(0), 60 * time.Second},
		{"configured, shorter argument", configured, chat, float64(5), 5 * time.Second},
		{"configured, longer argument", configured, chat, float64(300), 300 * time.Second},
		{"configured, argument capped at ten times", configured, chat, float64(100000), 600 * time.Second},
		{"unconfigured terminal turn has no timeout", unconfigured, cli, nil, 0},
		{"unconfigured chat turn gets the default", unconfigured, chat, nil, defaultExecTimeout},
		{"unconfigured chat turn, zero is not infinite", unconfigured, chat, float64(0), defaultExecTimeout},
		{"unconfigured chat turn, argument capped", unconfigured, chat, float64(100000), 10 * defaultExecTimeout},
		{"unconfigured turn without channel gets the default", unconfigured, context.Background(), nil, defaultExecTimeout},
		{"negative argument is ignored", configured, chat, float64(-3), 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := map[string]any{}
			if tt.arg != nil {
				args["timeout"] = tt.arg
			}
			require.Equal(t, tt.want, tt.tool.commandTimeout(tt.ctx, args))
		})
	}

	// A timeout set explicitly to none -- as cron does when configured so --
	// stays none in chat turns too.
	explicit, err := NewExecTool("", false)
	require.NoError(t, err)
	explicit.SetTimeout(0)
	require.Equal(t, time.Duration(0), explicit.commandTimeout(chat, map[string]any{}))
}

func TestExecTool_ParametersDescribeTimeoutLimits(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Exec.TimeoutSeconds = 30
	tool, err := NewExecToolWithConfig("", false, cfg)
	require.NoError(t, err)

	props := tool.Parameters()["properties"].(map[string]any)
	desc := props["timeout"].(map[string]any)["description"].(string)
	require.Contains(t, desc, "30s")
	require.Contains(t, desc, "300s")
	require.NotContains(t, desc, "no timeout")
}

// TestExecTool_TimeoutArgumentIsHonored verifies that a run stops at the
// timeout the command asked for, not at the configured one.
func TestExecTool_TimeoutArgumentIsHonored(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Exec.TimeoutSeconds = 30
	tool, err := NewExecToolWithConfig(t.TempDir(), false, cfg)
	require.NoError(t, err)

	command := "sleep 20"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 20"
	}

	start := time.Now()
	result := tool.Execute(WithToolContext(context.Background(), "cli", "direct"), map[string]any{
		"action":  "run",
		"command": command,
		"timeout": float64(1),
	})
	elapsed := time.Since(start)

	require.True(t, result.IsError, "expected a timeout, got %s", result.ForLLM)
	require.Contains(t, result.ForLLM, "timed out after 1s")
	require.Less(t, elapsed, 15*time.Second)
}

// TestExecTool_ChannelArgumentDoesNotLiftRemoteRestriction verifies that the
// model cannot claim an internal channel through the arguments.
func TestExecTool_ChannelArgumentDoesNotLiftRemoteRestriction(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Exec.EnableDenyPatterns = true
	cfg.Tools.Exec.AllowRemote = false

	tool, err := NewExecToolWithConfig("", false, cfg)
	require.NoError(t, err)

	result := tool.Execute(context.Background(), map[string]any{
		"action":    "run",
		"command":   "echo hi",
		"__channel": "cli",
	})
	require.True(t, result.IsError)
	require.Contains(t, result.ForLLM, "restricted to internal channels")
}

// TestExecTool_LargeOutputIsBounded verifies that a command printing far more
// than the capture limit returns a bounded, truncated result.
func TestExecTool_LargeOutputIsBounded(t *testing.T) {
	tool, err := NewExecTool(t.TempDir(), false)
	require.NoError(t, err)

	// About 3 MiB of output, three times the capture limit.
	command := "yes 0123456789012345678901234567890123456789 | head -c 3145728"
	if runtime.GOOS == "windows" {
		command = "$line = '0' * 1023; foreach ($i in 1..3072) { [Console]::Out.WriteLine($line) }"
	}

	result := tool.Execute(WithToolContext(context.Background(), "cli", "direct"), map[string]any{
		"action":  "run",
		"command": command,
		"timeout": float64(60),
	})
	require.False(t, result.IsError, truncateExecOutput(result.ForLLM))
	require.Contains(t, result.ForLLM, "(truncated,")
	require.Less(t, utf8.RuneCountInString(result.ForLLM), maxExecResultChars+200)
}
