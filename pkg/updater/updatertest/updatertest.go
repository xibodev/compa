// Package updatertest serves fake GitHub releases of xibodev/compa, shaped
// like the ones the release workflow publishes, for tests of the updater and
// of the endpoints built on it.
package updatertest

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// NoDigest as an Asset's Digest makes the fake API report no digest for it.
const NoDigest = "none"

// Asset is one file attached to a fake release.
type Asset struct {
	Name string
	Body []byte
	// Digest is what the API reports as the asset's digest: empty reports the
	// real SHA-256 of Body, NoDigest reports none, anything else is reported
	// verbatim.
	Digest string
}

// Release is one published release.
type Release struct {
	Tag    string
	Assets []Asset
}

// Server answers the GitHub releases API of xibodev/compa and serves the
// assets. The first release is the latest one. Point the updater's
// APIBaseURL at the returned server's URL.
func Server(t testing.TB, releases ...Release) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	byTag := make(map[string]Release, len(releases))
	for _, r := range releases {
		byTag[r.Tag] = r
	}
	answer := func(w http.ResponseWriter, r Release) {
		type apiAsset struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Size   int    `json:"size"`
			Digest string `json:"digest,omitempty"`
		}
		assets := make([]apiAsset, 0, len(r.Assets))
		for _, a := range r.Assets {
			digest := a.Digest
			switch digest {
			case "":
				digest = "sha256:" + SHA256(a.Body)
			case NoDigest:
				digest = ""
			}
			assets = append(assets, apiAsset{
				Name:   a.Name,
				URL:    fmt.Sprintf("%s/download/%s/%s", srv.URL, r.Tag, a.Name),
				Size:   len(a.Body),
				Digest: digest,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name":   r.Tag,
			"draft":      false,
			"prerelease": false,
			"assets":     assets,
		})
	}

	mux.HandleFunc("GET /repos/xibodev/compa/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		if len(releases) == 0 {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		answer(w, releases[0])
	})
	mux.HandleFunc("GET /repos/xibodev/compa/releases/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		rel, ok := byTag[r.PathValue("tag")]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		answer(w, rel)
	})
	mux.HandleFunc("GET /download/{tag}/{name}", func(w http.ResponseWriter, r *http.Request) {
		for _, a := range byTag[r.PathValue("tag")].Assets {
			if a.Name == r.PathValue("name") {
				w.Header().Set("Content-Type", "application/octet-stream")
				_, _ = w.Write(a.Body)
				return
			}
		}
		http.NotFound(w, r)
	})
	return srv
}

// ArchiveName is the asset name a release uses for goos/goarch.
func ArchiveName(tag, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("compa_%s_%s_%s%s", strings.TrimPrefix(tag, "v"), goos, goarch, ext)
}

// Programs is the content of a release archive for goos: compa and
// compa-kernel (with ".exe" on Windows) plus LICENSE and NOTICE.
func Programs(goos, launcher, kernel string) map[string]string {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	return map[string]string{
		"compa" + ext:        launcher,
		"compa-kernel" + ext: kernel,
		"LICENSE":            "MIT License\n",
		"NOTICE":             "Compa\n",
	}
}

// Archive packs files the way the release workflow does: a zip for Windows,
// a tar.gz otherwise, every file at the root.
func Archive(t testing.TB, goos string, files map[string]string) []byte {
	t.Helper()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	mode := func(name string) int64 {
		if strings.HasPrefix(name, "compa") {
			return 0o755
		}
		return 0o644
	}
	modTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	var buf bytes.Buffer
	if goos == "windows" {
		zw := zip.NewWriter(&buf)
		for _, name := range names {
			w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modTime})
			if err != nil {
				t.Fatalf("zip %s: %v", name, err)
			}
			if _, err := w.Write([]byte(files[name])); err != nil {
				t.Fatalf("zip %s: %v", name, err)
			}
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip: %v", err)
		}
		return buf.Bytes()
	}

	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range names {
		body := files[name]
		hdr := &tar.Header{
			Name: name, Mode: mode(name), Size: int64(len(body)),
			ModTime: modTime, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip: %v", err)
	}
	return buf.Bytes()
}

// Checksums is a SHA256SUMS asset covering assets, in sha256sum's format.
func Checksums(assets ...Asset) Asset {
	var b strings.Builder
	for _, a := range assets {
		fmt.Fprintf(&b, "%s  %s\n", SHA256(a.Body), a.Name)
	}
	return Asset{Name: "SHA256SUMS", Body: []byte(b.String())}
}

// Build is a complete release for goos/goarch: the archive holding the two
// programs with the given contents, and SHA256SUMS.
func Build(t testing.TB, tag, goos, goarch, launcher, kernel string) Release {
	t.Helper()
	archive := Asset{
		Name: ArchiveName(tag, goos, goarch),
		Body: Archive(t, goos, Programs(goos, launcher, kernel)),
	}
	return Release{Tag: tag, Assets: []Asset{archive, Checksums(archive)}}
}

// SHA256 is the lowercase hex SHA-256 of b.
func SHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
