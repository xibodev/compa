package logger

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestRedactMasksSecretsInText(t *testing.T) {
	RegisterSecret("sk-live-1234567890")
	for input, want := range map[string]string{
		"key sk-live-1234567890 used":                                             "key [REDACTED] used",
		"proxy http://user:p4ss@proxy.local:8080/x":                               "proxy http://[REDACTED]@proxy.local:8080/x",
		"GET https://h/v1?api_key=abc123&model=m":                                 "GET https://h/v1?api_key=[REDACTED]&model=m",
		"https://h/cb?code=1&access_token=xyz&Password=pw":                        "https://h/cb?code=1&access_token=[REDACTED]&Password=[REDACTED]",
		"https://h/?secret=s3cr3t#frag":                                           "https://h/?secret=[REDACTED]#frag",
		"GET https://api.telegram.org/bot123456:AAH-abcdefghijklmnop_qrstu/getMe": "GET https://api.telegram.org/bot123456:[REDACTED]/getMe",
		"nothing to hide here":                                                    "nothing to hide here",
		"https://example.com/path?q=search":                                       "https://example.com/path?q=search",
	} {
		if got := Redact(input); got != want {
			t.Errorf("Redact(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRedactMasksALongerSecretWhole(t *testing.T) {
	RegisterSecret("abcd-short")
	RegisterSecret("abcd-short-and-longer")
	if got := Redact("x abcd-short-and-longer y"); got != "x [REDACTED] y" {
		t.Fatalf("Redact() = %q", got)
	}
}

func TestRedactIgnoresTooShortSecrets(t *testing.T) {
	RegisterSecret("abc")
	RegisterSecret("   ")
	if got := Redact("abc and spaces"); got != "abc and spaces" {
		t.Fatalf("Redact() = %q, want a short value left alone", got)
	}
}

func TestSetRedactionTurnsRedactionOff(t *testing.T) {
	RegisterSecret("turn-off-secret")
	SetRedaction(false)
	t.Cleanup(func() { SetRedaction(true) })
	if got := Redact("turn-off-secret"); got != "turn-off-secret" {
		t.Fatalf("Redact() with redaction off = %q", got)
	}
	SetRedaction(true)
	if got := Redact("turn-off-secret"); got != redactedText {
		t.Fatalf("Redact() with redaction on = %q", got)
	}
}

// useLogFile sends the log to a new file for the rest of the test and
// returns a function reading what it holds.
func useLogFile(t *testing.T) (string, func() string) {
	t.Helper()
	initial := GetLevel()
	SetLevel(INFO)
	t.Cleanup(func() { SetLevel(initial) })
	path := filepath.Join(t.TempDir(), "logs", "test.log")
	if err := EnableFileLogging(path); err != nil {
		t.Fatalf("EnableFileLogging() error = %v", err)
	}
	t.Cleanup(DisableFileLogging)
	return path, func() string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		return string(data)
	}
}

func TestLogLinesAreRedacted(t *testing.T) {
	RegisterSecret("tok-registered-secret")
	_, read := useLogFile(t)

	InfoCF("test", "calling https://api.telegram.org/bot42:ABCDEFGHIJKLMNOPQRST/sendMessage", map[string]any{
		"url":     "https://me:hunter2@example.com/?token=tok-query",
		"error":   errors.New("upstream refused tok-registered-secret"),
		"headers": map[string]any{"Authorization": "Bearer tok-registered-secret"},
		"count":   3,
	})

	logged := read()
	for _, secret := range []string{"ABCDEFGHIJKLMNOPQRST", "hunter2", "tok-query", "tok-registered-secret"} {
		if strings.Contains(logged, secret) {
			t.Errorf("log line shows %q:\n%s", secret, logged)
		}
	}
	if !strings.Contains(logged, "bot42:[REDACTED]") || !strings.Contains(logged, `"count":3`) {
		t.Errorf("log line lost what is not secret:\n%s", logged)
	}
}

func TestEnableFileLoggingAgainMovesToTheNewFile(t *testing.T) {
	first, readFirst := useLogFile(t)
	Info("to the first file")
	second := filepath.Join(t.TempDir(), "second.log")
	if err := EnableFileLogging(second); err != nil {
		t.Fatalf("EnableFileLogging() again: error = %v", err)
	}
	Info("to the second file")

	data, err := os.ReadFile(second)
	if err != nil || !strings.Contains(string(data), "to the second file") {
		t.Fatalf("second file = %q, %v", data, err)
	}
	if logged := readFirst(); !strings.Contains(logged, "to the first file") || strings.Contains(logged, "to the second file") {
		t.Fatalf("first file %s = %q", first, logged)
	}
}

func TestEnableFileLoggingKeepsTheFileWhenTheNewOneFails(t *testing.T) {
	_, read := useLogFile(t)
	blocked := filepath.Join(t.TempDir(), "dir")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := EnableFileLogging(blocked); err == nil {
		t.Fatal("EnableFileLogging() on a directory succeeded")
	}
	Info("still logged")
	if !strings.Contains(read(), "still logged") {
		t.Fatal("a failed EnableFileLogging stopped file logging")
	}
}

func TestLogFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	path, _ := useLogFile(t)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("log file mode = %o, want 600", mode)
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dir.Mode().Perm(); mode != 0o700 {
		t.Fatalf("log directory mode = %o, want 700", mode)
	}
}

func TestConcurrentLoggingAndReconfiguration(t *testing.T) {
	initial := GetLevel()
	t.Cleanup(func() { SetLevel(initial) })
	useLogFile(t)
	dir := t.TempDir()

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 200 {
				InfoCF("race", "message", map[string]any{"i": i})
			}
		}()
		go func() {
			defer wg.Done()
			for j := range 20 {
				SetLevel([]LogLevel{DEBUG, INFO}[j%2])
				SetConsoleLevel(DEBUG)
				if err := EnableFileLogging(filepath.Join(dir, "concurrent.log")); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestInitPanicReportsALogItCannotOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panic.log")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	recoverFn, err := InitPanic(path)
	if err == nil || recoverFn != nil {
		t.Fatalf("InitPanic() = %v, %v; want an error", recoverFn != nil, err)
	}
}
