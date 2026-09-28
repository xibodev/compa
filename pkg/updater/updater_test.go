package updater

import (
	"bytes"
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/minio/selfupdate"

	"github.com/xibodev/compa/pkg/updater/updatertest"
)

func fakeOptions(srv *httptest.Server, dir string) Options {
	return Options{Dir: dir, APIBaseURL: srv.URL, HTTPClient: srv.Client()}
}

// installed writes an existing install of both programs into a new directory
// and returns it with the contents it holds.
func installed(t *testing.T, goos, launcher, kernel string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	progs := programsIn(dir, goos)
	files := map[string]string{
		filepath.Base(progs[0].target): kernel,
		filepath.Base(progs[1].target): launcher,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir, files
}

// assertDir fails unless dir holds exactly want (name -> content): no
// leftover prepared files, backups or strays.
func assertDir(t *testing.T, dir string, want map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]string, len(entries))
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		got[e.Name()] = string(body)
	}
	if len(got) != len(want) {
		t.Fatalf("directory holds %v, want exactly %v", keys(got), keys(want))
	}
	for name, body := range want {
		if got[name] != body {
			t.Fatalf("%s = %q, want %q (directory holds %v)", name, got[name], body, keys(got))
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestUpdateReplacesBothProgramsFromOneArchive(t *testing.T) {
	for _, tc := range []struct{ goos, goarch string }{
		{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "arm64"}, {"windows", "amd64"}, {"windows", "arm64"},
	} {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			srv := updatertest.Server(t,
				updatertest.Build(t, "v1.2.3", tc.goos, tc.goarch, "launcher 1.2.3", "kernel 1.2.3"))
			dir, files := installed(t, tc.goos, "launcher 1.0.0", "kernel 1.0.0")

			res, err := update(context.Background(), fakeOptions(srv, dir), tc.goos, tc.goarch)
			if err != nil {
				t.Fatalf("update: %v", err)
			}

			for name := range files {
				files[name] = strings.Replace(files[name], "1.0.0", "1.2.3", 1)
			}
			assertDir(t, dir, files)
			wantArchive := updatertest.ArchiveName("v1.2.3", tc.goos, tc.goarch)
			if res.Tag != "v1.2.3" || res.Version != "1.2.3" || res.Archive != wantArchive || res.Dir != dir {
				t.Fatalf("result = %+v, want tag v1.2.3, version 1.2.3, archive %s, dir %s", res, wantArchive, dir)
			}
		})
	}
}

func TestUpdateInstallsTheRequestedRelease(t *testing.T) {
	srv := updatertest.Server(t,
		updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher 1.2.3", "kernel 1.2.3"),
		updatertest.Build(t, "v1.1.0", "linux", "amd64", "launcher 1.1.0", "kernel 1.1.0"),
	)
	dir, _ := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")

	opts := fakeOptions(srv, dir)
	opts.Tag = "1.1.0"
	res, err := update(context.Background(), opts, "linux", "amd64")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if res.Tag != "v1.1.0" {
		t.Fatalf("installed %s, want v1.1.0", res.Tag)
	}
	assertDir(t, dir, map[string]string{"compa": "launcher 1.1.0", "compa-kernel": "kernel 1.1.0"})
}

func TestUpdateInstallsAMissingKernelBesideTheLauncher(t *testing.T) {
	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher 1.2.3", "kernel 1.2.3"))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "compa"), []byte("launcher 1.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := update(context.Background(), fakeOptions(srv, dir), "linux", "amd64"); err != nil {
		t.Fatalf("update: %v", err)
	}
	assertDir(t, dir, map[string]string{"compa": "launcher 1.2.3", "compa-kernel": "kernel 1.2.3"})
}

func TestUpdateRefusesAnArchiveThatDoesNotMatchSHA256SUMS(t *testing.T) {
	rel := updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher 1.2.3", "kernel 1.2.3")
	archive := &rel.Assets[0]
	// GitHub and SHA256SUMS agree on the published archive, but the bytes that
	// arrive are different -- a corrupted or substituted download.
	archive.Digest = "sha256:" + updatertest.SHA256(archive.Body)
	archive.Body = updatertest.Archive(t, "linux", updatertest.Programs("linux", "evil launcher", "evil kernel"))
	srv := updatertest.Server(t, rel)
	dir, files := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)

	_, err := update(context.Background(), fakeOptions(srv, dir), "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("update error = %v, want a SHA-256 mismatch", err)
	}
	assertDir(t, dir, files)
	assertDir(t, tmp, map[string]string{}) // the rejected download is gone
}

func TestUpdateRefusesAnArchiveSHA256SUMSDoesNotList(t *testing.T) {
	rel := updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher 1.2.3", "kernel 1.2.3")
	rel.Assets[1] = updatertest.Checksums(updatertest.Asset{Name: "compa_1.2.3_linux_arm64.tar.gz", Body: []byte("x")})
	srv := updatertest.Server(t, rel)
	dir, files := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")

	_, err := update(context.Background(), fakeOptions(srv, dir), "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "does not list compa_1.2.3_linux_amd64.tar.gz") {
		t.Fatalf("update error = %v, want SHA256SUMS not listing the archive", err)
	}
	assertDir(t, dir, files)
}

func TestUpdateRefusesWhenGitHubAndSHA256SUMSDisagree(t *testing.T) {
	rel := updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher 1.2.3", "kernel 1.2.3")
	rel.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
	srv := updatertest.Server(t, rel)
	dir, files := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")

	_, err := update(context.Background(), fakeOptions(srv, dir), "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("update error = %v, want a digest disagreement", err)
	}
	assertDir(t, dir, files)
}

func TestUpdateFailsWithoutSHA256SUMS(t *testing.T) {
	rel := updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher 1.2.3", "kernel 1.2.3")
	rel.Assets = rel.Assets[:1]
	srv := updatertest.Server(t, rel)
	dir, files := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")

	_, err := update(context.Background(), fakeOptions(srv, dir), "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "has no SHA256SUMS") {
		t.Fatalf("update error = %v, want a missing SHA256SUMS", err)
	}
	assertDir(t, dir, files)
}

func TestUpdateFailsWithoutABuildForThisPlatform(t *testing.T) {
	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher", "kernel"))
	dir, files := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")

	_, err := update(context.Background(), fakeOptions(srv, dir), "linux", "riscv64")
	if err == nil || !strings.Contains(err.Error(), "no build for linux/riscv64") {
		t.Fatalf("update error = %v, want no build for linux/riscv64", err)
	}
	assertDir(t, dir, files)
}

func TestUpdateFailsWhenTheArchiveLacksAProgram(t *testing.T) {
	archive := updatertest.Asset{
		Name: updatertest.ArchiveName("v1.2.3", "windows", "amd64"),
		Body: updatertest.Archive(t, "windows", map[string]string{"compa.exe": "launcher 1.2.3"}),
	}
	srv := updatertest.Server(t,
		updatertest.Release{Tag: "v1.2.3", Assets: []updatertest.Asset{archive, updatertest.Checksums(archive)}})
	dir, files := installed(t, "windows", "launcher 1.0.0", "kernel 1.0.0")

	_, err := update(context.Background(), fakeOptions(srv, dir), "windows", "amd64")
	if err == nil || !strings.Contains(err.Error(), "does not contain compa-kernel.exe") {
		t.Fatalf("update error = %v, want the missing kernel reported", err)
	}
	assertDir(t, dir, files) // the launcher prepared before the check is gone too
}

func TestUpdateReportsAMissingRelease(t *testing.T) {
	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher", "kernel"))
	dir, _ := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")

	opts := fakeOptions(srv, dir)
	opts.Tag = "v9.9.9"
	_, err := update(context.Background(), opts, "linux", "amd64")
	if err == nil || !strings.Contains(err.Error(), "release v9.9.9 of xibodev/compa was not found") {
		t.Fatalf("update error = %v, want release v9.9.9 not found", err)
	}
}

// The kernel is replaced first and the launcher last. When the launcher
// cannot be replaced, the kernel goes back, so the two never end up on
// different versions.
func TestUpdatePutsTheKernelBackWhenTheLauncherCannotBeReplaced(t *testing.T) {
	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", "windows", "amd64", "launcher 1.2.3", "kernel 1.2.3"))
	dir, files := installed(t, "windows", "launcher 1.0.0", "kernel 1.0.0")

	var order []string
	orig := commitBinary
	t.Cleanup(func() { commitBinary = orig })
	commitBinary = func(opts selfupdate.Options) error {
		order = append(order, filepath.Base(opts.TargetPath))
		if filepath.Base(opts.TargetPath) == "compa.exe" {
			return errors.New("the file is in use")
		}
		return orig(opts)
	}

	_, err := update(context.Background(), fakeOptions(srv, dir), "windows", "amd64")
	if err == nil || !strings.Contains(err.Error(), "the file is in use") {
		t.Fatalf("update error = %v, want the launcher failure", err)
	}
	if !slices.Equal(order[:2], []string{"compa-kernel.exe", "compa.exe"}) {
		t.Fatalf("replacement order = %v, want the kernel first", order)
	}
	assertDir(t, dir, files)
}

// After an update on Windows, the previous launcher keeps running from its
// backup until Compa restarts, and a running program cannot be deleted. An
// open handle blocks deletion the same way, so this holds one on the backup
// and updates again: the new backup must go beside it under its own name.
func TestUpdateWorksWhileAnEarlierBackupIsStillInUse(t *testing.T) {
	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher 1.2.3", "kernel 1.2.3"))
	dir, _ := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")
	backup := filepath.Join(dir, "compa.old")
	if err := os.WriteFile(backup, []byte("launcher 0.9.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(backup)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	if _, err := update(context.Background(), fakeOptions(srv, dir), "linux", "amd64"); err != nil {
		t.Fatalf("update: %v", err)
	}

	want := map[string]string{"compa": "launcher 1.2.3", "compa-kernel": "kernel 1.2.3"}
	if _, err := os.Stat(backup); err == nil {
		want["compa.old"] = "launcher 0.9.0" // Windows: still in use, removed by a later update
	}
	assertDir(t, dir, want)
}

func TestUpdateLogsEachStep(t *testing.T) {
	srv := updatertest.Server(t, updatertest.Build(t, "v1.2.3", "linux", "amd64", "launcher", "kernel"))
	dir, _ := installed(t, "linux", "launcher 1.0.0", "kernel 1.0.0")
	var log bytes.Buffer
	opts := fakeOptions(srv, dir)
	opts.Log = &log

	if _, err := update(context.Background(), opts, "linux", "amd64"); err != nil {
		t.Fatalf("update: %v", err)
	}
	for _, want := range []string{"latest release", "Downloading compa_1.2.3_linux_amd64.tar.gz", "Verified", "compa-kernel"} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("log %q does not mention %q", log.String(), want)
		}
	}
}

func TestArchiveName(t *testing.T) {
	for _, tc := range []struct{ version, goos, goarch, want string }{
		{"v1.2.3", "windows", "amd64", "compa_1.2.3_windows_amd64.zip"},
		{"1.2.3", "windows", "arm64", "compa_1.2.3_windows_arm64.zip"},
		{"v0.1.0", "linux", "arm64", "compa_0.1.0_linux_arm64.tar.gz"},
		{"v2.0.0", "darwin", "amd64", "compa_2.0.0_darwin_amd64.tar.gz"},
	} {
		if got := ArchiveName(tc.version, tc.goos, tc.goarch); got != tc.want {
			t.Errorf("ArchiveName(%q, %q, %q) = %q, want %q", tc.version, tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestNormalizeTag(t *testing.T) {
	for in, want := range map[string]string{"": "", "1.2.3": "v1.2.3", " v1.2.3 ": "v1.2.3", "v1.2.3-rc.1": "v1.2.3-rc.1"} {
		if got, err := NormalizeTag(in); err != nil || got != want {
			t.Errorf("NormalizeTag(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"latest", "v1.2", "1.2.3/../x", "v01.2.3", "nightly"} {
		if got, err := NormalizeTag(in); err == nil {
			t.Errorf("NormalizeTag(%q) = %q, want an error", in, got)
		}
	}
}

func TestTagFromURLAcceptsOnlyCompaReleases(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "",
		"https://github.com/xibodev/compa": "",
		"https://github.com/xibodev/compa/releases":                         "",
		"https://github.com/xibodev/compa/releases/latest":                  "",
		"https://github.com/XiboDev/Compa/releases/tag/v1.2.3/":             "v1.2.3",
		"https://api.github.com/repos/xibodev/compa/releases/latest":        "",
		"https://api.github.com/repos/xibodev/compa/releases/tags/v2.0.1":   "v2.0.1",
		"  https://github.com/xibodev/compa/releases/tag/1.0.0  ":           "v1.0.0",
		"https://www.github.com/xibodev/compa/releases/tag/v1.0.0?x=1#frag": "v1.0.0",
	} {
		if got, err := TagFromURL(in); err != nil || got != want {
			t.Errorf("TagFromURL(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{
		"http://github.com/xibodev/compa/releases/latest",
		"https://github.com/someone/compa/releases/latest",
		"https://github.com/xibodev/other/releases/latest",
		"https://evil.example/api.github.com/repos/xibodev/compa/releases/latest",
		"https://api.github.com.evil.example/repos/xibodev/compa/releases/latest",
		"https://user@github.com/xibodev/compa/releases/latest",
		"https://github.com/xibodev/compa/releases/download/v1.2.3/compa_1.2.3_linux_amd64.tar.gz",
		"https://github.com/xibodev/compa/releases/tag/nightly",
		"file:///tmp/compa.tar.gz",
	} {
		if got, err := TagFromURL(in); err == nil {
			t.Errorf("TagFromURL(%q) = %q, want an error", in, got)
		}
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := []byte(strings.Repeat("0", 64) + "  compa_1.2.3_linux_arm64.tar.gz\n" +
		strings.ToUpper(sum) + " *compa_1.2.3_linux_amd64.tar.gz\r\n")
	if got, err := checksumFor(sums, "compa_1.2.3_linux_amd64.tar.gz"); err != nil || got != sum {
		t.Fatalf("checksumFor = %q, %v; want %q", got, err, sum)
	}
	if _, err := checksumFor(sums, "compa_1.2.3_windows_amd64.zip"); err == nil {
		t.Fatal("checksumFor found an archive SHA256SUMS does not list")
	}
	if _, err := checksumFor([]byte("abc  compa.zip\n"), "compa.zip"); err == nil {
		t.Fatal("checksumFor accepted a malformed checksum")
	}
}
