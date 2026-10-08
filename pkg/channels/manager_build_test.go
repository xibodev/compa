package channels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xibodev/compa/v3/pkg/config"
	"github.com/xibodev/compa/v3/pkg/logger"
)

// An enabled channel the build leaves out is reported with the build tag
// that adds it; a type nothing registers, as before.
func TestInitChannelNamesTheBuildTagOfAChannelNotInTheBuild(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "channels.log")
	prevLevel := logger.GetLevel()
	logger.SetLevel(logger.WARN)
	if err := logger.EnableFileLogging(logFile); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	t.Cleanup(func() {
		logger.DisableFileLogging()
		logger.SetLevel(prevLevel)
	})

	// No channel package registers a factory in this package's tests.
	m := newTestManager()
	m.initChannel(config.ChannelOneBot, "my_onebot")
	m.initChannel(config.ChannelWhatsAppNative, "whatsapp")
	m.initChannel("not_a_channel", "mine")
	logger.DisableFileLogging()

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for line := range strings.Lines(string(data)) {
		lines = append(lines, line)
	}
	if len(lines) != 3 {
		t.Fatalf("logged %d lines, want 3:\n%s", len(lines), data)
	}
	for i, want := range [][]string{
		{"Channel not in this build", `"channel":"my_onebot"`, `"type":"onebot"`, `"build_tag":"paused_channels"`},
		{"Channel not in this build", `"channel":"whatsapp"`, `"type":"whatsapp_native"`, `"build_tag":"whatsapp_native"`},
		{"Factory not registered", `"channel":"mine"`, `"type":"not_a_channel"`},
	} {
		for _, part := range want {
			if !strings.Contains(lines[i], part) {
				t.Errorf("line %d = %s, want it to contain %s", i+1, lines[i], part)
			}
		}
	}
	if strings.Contains(lines[2], "build_tag") {
		t.Errorf("a type nothing adds names a build tag: %s", lines[2])
	}
	if len(m.channels) != 0 {
		t.Fatalf("channels = %v, want none", m.channels)
	}
}
