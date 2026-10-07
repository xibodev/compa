package logger

import (
	"bytes"
	"encoding/json"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestCallerPathIsRelativeToTheModule(t *testing.T) {
	if modulePrefix == "" {
		t.Fatal("the module's source directory is unknown")
	}
	for file, want := range map[string]string{
		modulePrefix + "pkg/agent/instance.go":                             "pkg/agent/instance.go",
		strings.ToUpper(modulePrefix[:1]) + modulePrefix[1:] + "x.go":      "x.go",
		`C:\Users\someone\go\pkg\mod\github.com\rs\zerolog@v1.35.1\log.go`: "github.com/rs/zerolog@v1.35.1/log.go",
		"/home/someone/go/pkg/mod/golang.org/x/net@v0.1.0/http2/frame.go":  "golang.org/x/net@v0.1.0/http2/frame.go",
		"elsewhere/file.go": "elsewhere/file.go",
	} {
		if got := callerPath(file); got != want {
			t.Errorf("callerPath(%q) = %q, want %q", file, got, want)
		}
	}
}

// Built with -trimpath, the binary records the module's files under the
// module path; they read the same.
func TestCallerPathUnderTrimpath(t *testing.T) {
	original := modulePrefix
	modulePrefix = "github.com/xibodev/compa/v3/"
	t.Cleanup(func() { modulePrefix = original })
	if got := callerPath("github.com/xibodev/compa/v3/pkg/agent/instance.go"); got != "pkg/agent/instance.go" {
		t.Fatalf("callerPath() = %q, want pkg/agent/instance.go", got)
	}
	if got := callerPath("github.com/rs/zerolog@v1.35.1/log.go"); got != "github.com/rs/zerolog@v1.35.1/log.go" {
		t.Fatalf("callerPath() = %q, want the dependency path kept", got)
	}
}

// A log line names its caller relative to the module.
func TestLogLinesNameTheirCallerRelativeToTheModule(t *testing.T) {
	var buf bytes.Buffer
	l := zerolog.New(&buf).With().Caller().Logger()
	_, _, line, _ := runtime.Caller(0)
	l.Info().Msg("where am I") // must stay on the line after runtime.Caller
	var got struct {
		Caller string `json:"caller"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if want := "pkg/logger/caller_test.go:" + strconv.Itoa(line+1); got.Caller != want {
		t.Fatalf("caller = %q, want %q", got.Caller, want)
	}
}

func TestFormatCallerKeepsThePathAsLogged(t *testing.T) {
	if got := formatCaller(false)("pkg/agent/instance.go:410"); got != "pkg/agent/instance.go:410 >" {
		t.Fatalf("plain caller = %q", got)
	}
	if got := formatCaller(true)("pkg/agent/instance.go:410"); !strings.Contains(got, "pkg/agent/instance.go:410") || !strings.Contains(got, "\x1b[") {
		t.Fatalf("colored caller = %q", got)
	}
	if got := formatCaller(false)(nil); got != "" {
		t.Fatalf("no caller = %q", got)
	}
}
