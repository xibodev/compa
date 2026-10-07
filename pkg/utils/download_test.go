package utils

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const secretPath = "/file/bot123456:AAH-secret-token-value/doc.pdf"

func TestLogSafeURLKeepsOnlySchemeAndHost(t *testing.T) {
	for raw, want := range map[string]string{
		"https://api.telegram.org" + secretPath + "?x=1": "https://api.telegram.org",
		"http://user:pw@127.0.0.1:8080/a":                "http://127.0.0.1:8080",
		"not a url":                                      "(url)",
	} {
		if got := LogSafeURL(raw); got != want {
			t.Errorf("LogSafeURL(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestDownloadToFileErrorShowsNoToken(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1"+secretPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DownloadToFile(context.Background(), &http.Client{Timeout: 2 * time.Second}, req, 0)
	if err == nil || strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "bot123456") {
		t.Fatalf("error = %v, want one without the URL's token", err)
	}
}

func TestDownloadsStopAtTheirSizeLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), 2048))
	}))
	defer server.Close()

	req, _ := http.NewRequest(http.MethodGet, server.URL+secretPath, nil)
	if path, err := DownloadToFile(context.Background(), server.Client(), req, 1024); err == nil {
		_ = os.Remove(path)
		t.Fatal("DownloadToFile() accepted a body over its limit")
	}
	if path := DownloadFile(server.URL+secretPath, "big.bin", DownloadOptions{MaxBytes: 1024}); path != "" {
		_ = os.Remove(path)
		t.Fatal("DownloadFile() accepted a body over its limit")
	}
	path := DownloadFile(server.URL+secretPath, "ok.bin", DownloadOptions{MaxBytes: 4096})
	if path == "" {
		t.Fatal("DownloadFile() refused a body within its limit")
	}
	defer os.Remove(path)
	if info, err := os.Stat(path); err != nil || info.Size() != 2048 {
		t.Fatalf("downloaded file = %v, %v", info, err)
	}
}

func writeZip(t *testing.T, files map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(out)
	for name, data := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractZipFileLimits(t *testing.T) {
	entries, total := maxZipEntries, maxZipTotalSize
	maxZipEntries, maxZipTotalSize = 20, 1000
	t.Cleanup(func() { maxZipEntries, maxZipTotalSize = entries, total })

	ok := writeZip(t, map[string][]byte{"skill/SKILL.md": []byte("# skill"), "skill/run.sh": []byte("echo hi")})
	if err := ExtractZipFile(ok, t.TempDir()); err != nil {
		t.Fatalf("ExtractZipFile() on a small archive: %v", err)
	}

	many := map[string][]byte{}
	for i := range maxZipEntries + 1 {
		many["d/"+strconv.Itoa(i)] = nil
	}
	if err := ExtractZipFile(writeZip(t, many), t.TempDir()); err == nil || !strings.Contains(err.Error(), errZipLimitsText) {
		t.Fatalf("ExtractZipFile() with %d entries = %v, want the entry limit", len(many), err)
	}

	// Entries within the per-file limit whose sum is past the total.
	big := map[string][]byte{}
	for i := range 3 {
		big["f"+strconv.Itoa(i)] = bytes.Repeat([]byte("z"), 400)
	}
	if err := ExtractZipFile(writeZip(t, big), t.TempDir()); err == nil || !strings.Contains(err.Error(), errZipLimitsText) {
		t.Fatalf("ExtractZipFile() past the total = %v, want the total limit", err)
	}
}

func TestDoRequestWithRetryResendsTheBody(t *testing.T) {
	retryDelayUnit = time.Millisecond
	t.Cleanup(func() { retryDelayUnit = time.Second })
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// A reader without GetBody, so the helper must keep the body itself.
	req, _ := http.NewRequest(http.MethodPost, server.URL, io.NopCloser(strings.NewReader(`{"q":1}`)))
	resp, err := DoRequestWithRetry(server.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if len(bodies) != 2 || bodies[0] != `{"q":1}` || bodies[1] != `{"q":1}` {
		t.Fatalf("bodies = %q, want the body sent whole twice", bodies)
	}
}

func TestDoRequestWithRetryDoesNotRetryNotImplemented(t *testing.T) {
	retryDelayUnit = time.Millisecond
	t.Cleanup(func() { retryDelayUnit = time.Second })
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer server.Close()
	req, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	resp, err := DoRequestWithRetry(server.Client(), req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 501 not retried", attempts)
	}
}

func TestRetryBackoffGrowsWithJitter(t *testing.T) {
	for attempt, base := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		delay := retryDelayForAttempt(nil, attempt)
		if delay < base || delay > base+base/2 {
			t.Fatalf("attempt %d: delay %v, want within [%v, %v]", attempt, delay, base, base+base/2)
		}
	}
}
