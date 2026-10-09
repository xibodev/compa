// Package updater installs releases of Compa published on
// github.com/xibodev/compa.
//
// Every release ships one archive per platform, compa_<version>_<os>_<arch>
// (.zip on Windows, .tar.gz elsewhere), holding BOTH programs -- compa, the
// launcher, and compa-kernel, the harness it supervises -- plus a SHA256SUMS
// file listing the SHA-256 of every asset. An update installs both programs
// from the same verified archive, so the launcher and the kernel beside it
// cannot drift onto different versions.
package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/minio/selfupdate"
	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v4/pkg/config"
	"github.com/xibodev/compa/v4/pkg/netbind"
	"github.com/xibodev/compa/v4/pkg/utils"
)

const (
	// Owner and Repo name the only repository updates come from.
	Owner = "xibodev"
	Repo  = "compa"

	// LauncherName and KernelName are the two programs every archive holds
	// (with ".exe" on Windows).
	LauncherName = "compa"
	KernelName   = "compa-kernel"

	// ChecksumsName is the release asset listing the SHA-256 of every other
	// asset, in the format sha256sum writes.
	ChecksumsName = "SHA256SUMS"

	defaultAPIBaseURL = "https://api.github.com"
	defaultTimeout    = 15 * time.Minute

	maxReleaseJSONBytes = 8 << 20
	maxChecksumsBytes   = 1 << 20
	maxArchiveBytes     = 512 << 20
	maxProgramBytes     = 512 << 20
)

// Options selects the release to install and where to install it.
type Options struct {
	// Tag is the release to install, such as "v1.2.3" ("1.2.3" works too).
	// Empty installs the latest release.
	Tag string

	// Dir is the directory holding compa and compa-kernel. Empty means the
	// directory of the running executable, which is where an install keeps
	// both programs.
	Dir string

	// Log receives one line per step for a person to read. Nil discards them.
	Log io.Writer

	// APIBaseURL replaces https://api.github.com and HTTPClient replaces the
	// default client. Tests point them at a fake release; nothing else
	// should set them.
	APIBaseURL string
	HTTPClient *http.Client

	// SelfTest replaces the check that the new compa-kernel runs (with
	// "version", it must exit successfully). Tests whose programs are not
	// real ones set it; nothing else should.
	SelfTest func(ctx context.Context, path string) error

	// AllowDowngrade installs a release older than the running version,
	// which is refused otherwise.
	AllowDowngrade bool
}

// Result describes an installed release.
type Result struct {
	Tag     string // release tag, e.g. "v1.2.3"
	Version string // the tag without its leading "v"
	Dir     string // directory holding the updated programs
	Archive string // the archive both programs came from
}

// Update installs one release of Compa into opts.Dir. It downloads the
// archive for the running OS and architecture, verifies it against the
// release's SHA256SUMS, and replaces compa-kernel and then compa -- the
// launcher last, because it is normally the program running the update.
// Both programs are replaced or neither is. Running programs keep the old
// version until they restart.
func Update(ctx context.Context, opts Options) (*Result, error) {
	return update(ctx, opts, runtime.GOOS, runtime.GOARCH)
}

// ArchiveName is the release asset holding both programs for goos/goarch.
func ArchiveName(version, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return fmt.Sprintf("compa_%s_%s_%s%s", strings.TrimPrefix(version, "v"), goos, goarch, ext)
}

var tagPattern = regexp.MustCompile(
	`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$`,
)

// NormalizeTag turns "1.2.3" or "v1.2.3" into the release tag "v1.2.3". An
// empty string stays empty and means the latest release.
func NormalizeTag(version string) (string, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return "", nil
	}
	if !tagPattern.MatchString(version) {
		return "", fmt.Errorf("%q is not a release version such as v1.2.3", version)
	}
	return "v" + strings.TrimPrefix(version, "v"), nil
}

// TagFromURL returns the release a github.com/xibodev/compa release URL
// names, or "" for the latest release. Updates never come from anywhere
// else, so any other URL is an error. Accepted forms:
//
//	https://github.com/xibodev/compa[/releases[/latest]]
//	https://github.com/xibodev/compa/releases/tag/<tag>
//	https://api.github.com/repos/xibodev/compa/releases/latest
//	https://api.github.com/repos/xibodev/compa/releases/tags/<tag>
func TagFromURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	foreign := fmt.Errorf("updates come only from https://github.com/%s/%s/releases, not %q", Owner, Repo, raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return "", foreign
	}
	parts := strings.FieldsFunc(u.Path, func(r rune) bool { return r == '/' })
	isRepo := func(owner, repo string) bool {
		return strings.EqualFold(owner, Owner) && strings.EqualFold(repo, Repo)
	}

	switch strings.ToLower(u.Host) {
	case "github.com", "www.github.com":
		if len(parts) < 2 || !isRepo(parts[0], parts[1]) {
			return "", foreign
		}
		rest := parts[2:]
		switch {
		case len(rest) == 0,
			len(rest) == 1 && rest[0] == "releases",
			len(rest) == 2 && rest[0] == "releases" && rest[1] == "latest":
			return "", nil
		case len(rest) == 3 && rest[0] == "releases" && rest[1] == "tag":
			return NormalizeTag(rest[2])
		}
	case "api.github.com":
		if len(parts) < 4 || parts[0] != "repos" || !isRepo(parts[1], parts[2]) || parts[3] != "releases" {
			return "", foreign
		}
		rest := parts[4:]
		switch {
		case len(rest) == 1 && rest[0] == "latest":
			return "", nil
		case len(rest) == 2 && rest[0] == "tags":
			return NormalizeTag(rest[1])
		}
	}
	return "", foreign
}

// errDowngrade refuses a release older than the running version unless
// Options.AllowDowngrade is set.
var errDowngrade = errors.New("installing it needs an explicit downgrade")

func update(ctx context.Context, opts Options, goos, goarch string) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
		defer cancel()
	}

	tag, err := NormalizeTag(opts.Tag)
	if err != nil {
		return nil, err
	}
	dir, err := installDir(opts.Dir)
	if err != nil {
		return nil, err
	}
	client := opts.HTTPClient
	if client == nil {
		client = defaultClient
	}
	base := strings.TrimRight(opts.APIBaseURL, "/")
	if base == "" {
		base = defaultAPIBaseURL
	}

	if tag == "" {
		opts.logf("Looking up the latest release of %s/%s...", Owner, Repo)
	} else {
		opts.logf("Looking up release %s of %s/%s...", tag, Owner, Repo)
	}
	rel, err := fetchRelease(ctx, client, base, tag)
	if err != nil {
		return nil, err
	}
	if current := config.GetVersion(); !opts.AllowDowngrade && CompareVersions(rel.TagName, current) < 0 {
		return nil, fmt.Errorf("release %s is older than the running version %s; %w", rel.TagName, current, errDowngrade)
	}

	version := strings.TrimPrefix(rel.TagName, "v")
	name := ArchiveName(version, goos, goarch)
	archive, ok := rel.asset(name)
	if !ok {
		return nil, fmt.Errorf("release %s has no build for %s/%s (no %s)", rel.TagName, goos, goarch, name)
	}
	sumsAsset, ok := rel.asset(ChecksumsName)
	if !ok {
		return nil, fmt.Errorf("release %s has no %s, so its archives cannot be verified", rel.TagName, ChecksumsName)
	}

	sums, err := downloadChecksums(ctx, client, sumsAsset)
	if err != nil {
		return nil, err
	}
	want, err := checksumFor(sums, name)
	if err != nil {
		return nil, err
	}
	reported, err := archive.sha256()
	if err != nil {
		return nil, err
	}
	if reported != "" && reported != want {
		return nil, fmt.Errorf("release %s disagrees with itself about %s: %s lists %s, GitHub reports %s",
			rel.TagName, name, ChecksumsName, want, reported)
	}

	opts.logf("Downloading %s (%s)...", name, humanBytes(archive.Size))
	archivePath, err := downloadArchive(ctx, client, archive, want)
	if err != nil {
		return nil, err
	}
	defer os.Remove(archivePath)
	opts.logf("Verified the SHA-256 of %s against %s.", name, ChecksumsName)

	progs := programsIn(dir, goos)
	if err := prepare(archivePath, name, progs); err != nil {
		return nil, err
	}
	done, err := commit(progs)
	if err != nil {
		return nil, err
	}
	// The new kernel must start before the update stands: one that does not
	// run on this machine is put back, with the launcher, at once.
	test := selfTest
	if opts.SelfTest != nil {
		test = opts.SelfTest
	}
	if err := test(ctx, progs[0].target); err != nil {
		testErr := fmt.Errorf("the new %s does not run (%v), so the previous version was put back", filepath.Base(progs[0].target), err)
		if rerr := rollback(done); rerr != nil {
			return nil, fmt.Errorf("%w; putting it back also failed, so reinstall Compa: %v", testErr, rerr)
		}
		return nil, testErr
	}
	removeBackups(done)
	for _, p := range progs {
		opts.logf("Updated %s.", p.target)
	}

	return &Result{Tag: rel.TagName, Version: version, Dir: dir, Archive: name}, nil
}

// selfTestTimeout bounds the self-test of a new program.
const selfTestTimeout = 30 * time.Second

// selfTest runs the program at path, newly installed, with "version": it
// must exit successfully. Tests replace it (Options.SelfTest), since their
// programs are not real ones.
var selfTest = func(ctx context.Context, path string) error {
	ctx, cancel := context.WithTimeout(ctx, selfTestTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CompareVersions compares two versions such as "v1.2.3" or "1.2.3-rc.1"
// as semantic versions: -1, 0 or +1. A version that is not one, such as a
// development build's, compares as equal to anything, so it never blocks.
func CompareVersions(a, b string) int {
	pa, okA := parseVersion(a)
	pb, okB := parseVersion(b)
	if !okA || !okB {
		return 0
	}
	for i := range 3 {
		if c := cmpInt(pa.core[i], pb.core[i]); c != 0 {
			return c
		}
	}
	switch {
	case pa.pre == "" && pb.pre == "":
		return 0
	case pa.pre == "":
		return 1 // a release follows its prereleases
	case pb.pre == "":
		return -1
	}
	ia, ib := strings.Split(pa.pre, "."), strings.Split(pb.pre, ".")
	for i := 0; i < len(ia) && i < len(ib); i++ {
		na, errA := strconv.Atoi(ia[i])
		nb, errB := strconv.Atoi(ib[i])
		var c int
		switch {
		case errA == nil && errB == nil:
			c = cmpInt(na, nb)
		case errA == nil:
			c = -1 // numeric identifiers sort first
		case errB == nil:
			c = 1
		default:
			c = strings.Compare(ia[i], ib[i])
		}
		if c != 0 {
			return c
		}
	}
	return cmpInt(len(ia), len(ib))
}

type parsedVersion struct {
	core [3]int
	pre  string
}

func parseVersion(v string) (parsedVersion, bool) {
	m := tagPattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return parsedVersion{}, false
	}
	var p parsedVersion
	for i := range 3 {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return parsedVersion{}, false
		}
		p.core[i] = n
	}
	p.pre = strings.TrimPrefix(m[4], "-")
	return p, true
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (o Options) logf(format string, args ...any) {
	if o.Log != nil {
		fmt.Fprintf(o.Log, format+"\n", args...)
	}
}

// installDir resolves where compa and compa-kernel live.
func installDir(dir string) (string, error) {
	if dir == "" {
		exe, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("find the running program: %w", err)
		}
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		dir = filepath.Dir(exe)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("install directory %s: %w", dir, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("install directory: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("install directory %s is not a directory", abs)
	}
	return abs, nil
}

// --- The release, as the GitHub API describes it ---------------------------

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

func (r *release) asset(name string) (asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return asset{}, false
}

// sha256 returns the SHA-256 GitHub reports for the asset, or "" when it
// reports none.
func (a asset) sha256() (string, error) {
	digest := strings.ToLower(strings.TrimSpace(a.Digest))
	hexSum, ok := strings.CutPrefix(digest, "sha256:")
	if !ok {
		return "", nil
	}
	if !isSHA256(hexSum) {
		return "", fmt.Errorf("GitHub reports a malformed digest %q for %s", a.Digest, a.Name)
	}
	return hexSum, nil
}

func fetchRelease(ctx context.Context, c *http.Client, base, tag string) (*release, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/%s/releases/latest", base, Owner, Repo)
	what := fmt.Sprintf("the latest release of %s/%s", Owner, Repo)
	if tag != "" {
		endpoint = fmt.Sprintf("%s/repos/%s/%s/releases/tags/%s", base, Owner, Repo, url.PathEscape(tag))
		what = fmt.Sprintf("release %s of %s/%s", tag, Owner, Repo)
	}
	body, err := fetch(ctx, c, endpoint, "application/vnd.github+json", maxReleaseJSONBytes)
	if err != nil {
		var se *statusError
		if errors.As(err, &se) && se.code == http.StatusNotFound {
			return nil, fmt.Errorf("%s was not found", what)
		}
		return nil, fmt.Errorf("look up %s: %w", what, err)
	}
	var rel release
	if err := json.Unmarshal(body, &rel); err != nil {
		return nil, fmt.Errorf("look up %s: unreadable answer: %w", what, err)
	}
	if rel.TagName == "" {
		return nil, fmt.Errorf("look up %s: the answer names no tag", what)
	}
	if tag != "" && rel.TagName != tag {
		return nil, fmt.Errorf("look up %s: GitHub answered with release %s", what, rel.TagName)
	}
	return &rel, nil
}

// --- Downloads ---------------------------------------------------------------

// defaultClient has no overall Timeout, because an archive on a slow link can
// take minutes; the update's context bounds the whole operation instead.
var defaultClient = &http.Client{Transport: defaultTransport()}

func defaultTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t = t.Clone()
	t.ResponseHeaderTimeout = time.Minute
	return t
}

type statusError struct {
	url  string
	code int
	hint string
}

func (e *statusError) Error() string {
	msg := fmt.Sprintf("GET %s: %d %s", e.url, e.code, http.StatusText(e.code))
	if e.hint != "" {
		msg += " (" + e.hint + ")"
	}
	return msg
}

func get(ctx context.Context, c *http.Client, rawURL, accept string) (*http.Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && (u.Scheme != "http" || !netbind.IsLoopbackHost(u.Hostname()))) {
		// Plain http only reaches this machine, as a test's fake release
		// does: anything on the network could alter it.
		return nil, fmt.Errorf("refusing to download from %q: updates come over https", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "compa-updater/"+config.GetVersion())
	req.Header.Set("Accept", accept)
	if strings.Contains(accept, "github") {
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	resp, err := utils.DoRequestWithRetry(c, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		se := &statusError{url: rawURL, code: resp.StatusCode}
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			if resp.Header.Get("X-RateLimit-Remaining") == "0" {
				se.hint = "GitHub API rate limit reached; try again later"
			}
		}
		return nil, se
	}
	return resp, nil
}

func fetch(ctx context.Context, c *http.Client, rawURL, accept string, limit int64) ([]byte, error) {
	resp, err := get(ctx, c, rawURL, accept)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", rawURL, limit)
	}
	return body, nil
}

func downloadChecksums(ctx context.Context, c *http.Client, a asset) ([]byte, error) {
	body, err := fetch(ctx, c, a.URL, "application/octet-stream", maxChecksumsBytes)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", ChecksumsName, err)
	}
	reported, err := a.sha256()
	if err != nil {
		return nil, err
	}
	if got := sha256Hex(body); reported != "" && got != reported {
		return nil, fmt.Errorf("SHA-256 mismatch for %s: downloaded %s, GitHub reports %s", ChecksumsName, got, reported)
	}
	return body, nil
}

// downloadArchive saves the archive to a temporary file and checks its SHA-256
// while it streams, so a corrupted or substituted archive never reaches the
// install directory.
func downloadArchive(ctx context.Context, c *http.Client, a asset, want string) (string, error) {
	if a.Size > maxArchiveBytes {
		return "", fmt.Errorf("%s is %s, larger than an update may be", a.Name, humanBytes(a.Size))
	}
	resp, err := get(ctx, c, a.URL, "application/octet-stream")
	if err != nil {
		return "", fmt.Errorf("download %s: %w", a.Name, err)
	}
	defer resp.Body.Close()

	f, err := os.CreateTemp("", "compa-update-*-"+a.Name)
	if err != nil {
		return "", err
	}
	fail := func(err error) (string, error) {
		_ = os.Remove(f.Name())
		return "", err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxArchiveBytes+1))
	closeErr := f.Close()
	switch {
	case copyErr != nil:
		return fail(fmt.Errorf("download %s: %w", a.Name, copyErr))
	case closeErr != nil:
		return fail(fmt.Errorf("save %s: %w", a.Name, closeErr))
	case n > maxArchiveBytes:
		return fail(fmt.Errorf("%s is larger than an update may be", a.Name))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fail(fmt.Errorf("SHA-256 mismatch for %s: downloaded %s but %s lists %s; nothing was changed",
			a.Name, got, ChecksumsName, want))
	}
	return f.Name(), nil
}

// checksumFor returns the SHA-256 that SHA256SUMS lists for name. Lines have
// sha256sum's format: "<hex>  <name>", or "<hex> *<name>" in binary mode.
func checksumFor(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		sum := strings.ToLower(fields[0])
		if !isSHA256(sum) {
			return "", fmt.Errorf("%s has a malformed entry for %s", ChecksumsName, name)
		}
		return sum, nil
	}
	return "", fmt.Errorf("%s does not list %s", ChecksumsName, name)
}

func isSHA256(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// --- Replacing the programs --------------------------------------------------

// program is one executable inside the archive and the file it replaces.
type program struct {
	entry  string // file name inside the archive
	target string // path in the install directory
}

// programsIn lists the programs in the order they are replaced: the kernel
// first, then the launcher. The launcher is normally the process running
// this update, so it goes last and a failure before it leaves it untouched.
func programsIn(dir, goos string) []program {
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	return []program{
		{entry: KernelName + ext, target: filepath.Join(dir, KernelName+ext)},
		{entry: LauncherName + ext, target: filepath.Join(dir, LauncherName+ext)},
	}
}

// preparedPath is where selfupdate.PrepareAndCheckBinary writes a new file
// before CommitBinary moves it into place.
func preparedPath(target string) string {
	return filepath.Join(filepath.Dir(target), "."+filepath.Base(target)+".new")
}

func discardPrepared(progs []program) {
	for _, p := range progs {
		_ = os.Remove(preparedPath(p.target))
	}
}

// prepare writes every program from the archive next to the file it will
// replace, without replacing anything yet. It fails unless the archive holds
// each program exactly once.
func prepare(archivePath, archiveName string, progs []program) (err error) {
	defer func() {
		if err != nil {
			discardPrepared(progs)
		}
	}()

	found := make(map[string]bool, len(progs))
	visit := func(name string, size int64, open func() (io.ReadCloser, error)) error {
		base := path.Base(path.Clean(strings.ReplaceAll(name, `\`, "/")))
		for _, p := range progs {
			if base != p.entry {
				continue
			}
			if found[p.entry] {
				return fmt.Errorf("%s holds more than one %s", archiveName, p.entry)
			}
			if size > maxProgramBytes {
				return fmt.Errorf("%s in %s is larger than a program may be", p.entry, archiveName)
			}
			r, err := open()
			if err != nil {
				return fmt.Errorf("read %s from %s: %w", p.entry, archiveName, err)
			}
			err = selfupdate.PrepareAndCheckBinary(r, selfupdate.Options{TargetPath: p.target, TargetMode: 0o755})
			_ = r.Close()
			if err != nil {
				return fmt.Errorf("write %s: %w", preparedPath(p.target), err)
			}
			found[p.entry] = true
		}
		return nil
	}

	if strings.HasSuffix(archiveName, ".zip") {
		err = walkZip(archivePath, visit)
	} else {
		err = walkTarGz(archivePath, visit)
	}
	if err != nil {
		return err
	}
	for _, p := range progs {
		if !found[p.entry] {
			return fmt.Errorf("%s does not contain %s", archiveName, p.entry)
		}
	}
	return nil
}

type entryVisitor func(name string, size int64, open func() (io.ReadCloser, error)) error

func walkZip(archivePath string, visit entryVisitor) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(archivePath), err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !f.Mode().IsRegular() {
			continue
		}
		if err := visit(f.Name, int64(f.UncompressedSize64), f.Open); err != nil {
			return err
		}
	}
	return nil
}

func walkTarGz(archivePath string, visit entryVisitor) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("open %s: %w", filepath.Base(archivePath), err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", filepath.Base(archivePath), err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		open := func() (io.ReadCloser, error) { return io.NopCloser(tr), nil }
		if err := visit(hdr.Name, hdr.Size, open); err != nil {
			return err
		}
	}
}

// committed records one replaced program, so it can be put back.
type committed struct {
	target string
	backup string // where the previous file went; "" when there was none
}

// commitBinary is selfupdate.CommitBinary; a variable so tests can make one
// replacement fail.
var commitBinary = selfupdate.CommitBinary

// commit moves the prepared programs into place in order. When one fails,
// the ones already replaced are put back, so both programs stay on the same
// version. It returns what it replaced, whose backups removeBackups removes
// once the update stands.
func commit(progs []program) ([]committed, error) {
	var done []committed
	for i, p := range progs {
		c, err := commitOne(p)
		if err != nil {
			discardPrepared(progs[i:])
			if rerr := rollback(done); rerr != nil {
				return nil, fmt.Errorf("%w; putting back the programs already replaced also failed, so reinstall Compa: %v",
					err, rerr)
			}
			return nil, err
		}
		done = append(done, c)
	}
	return done, nil
}

func removeBackups(done []committed) {
	for _, c := range done {
		if c.backup != "" {
			// A program that is still running cannot be deleted on Windows;
			// the next update or install removes it.
			_ = os.Remove(c.backup)
		}
	}
}

func commitOne(p program) (committed, error) {
	if _, err := os.Lstat(p.target); errors.Is(err, fs.ErrNotExist) {
		if err := os.Rename(preparedPath(p.target), p.target); err != nil {
			return committed{}, fmt.Errorf("install %s: %w", p.target, err)
		}
		return committed{target: p.target}, nil
	}

	removeStaleBackups(p.target)
	backup := backupPath(p.target)
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// Scanners briefly lock freshly written executables on Windows.
			time.Sleep(time.Duration(attempt) * 300 * time.Millisecond)
		}
		err = commitBinary(selfupdate.Options{TargetPath: p.target, OldSavePath: backup})
		if err == nil {
			return committed{target: p.target, backup: backup}, nil
		}
		if rerr := selfupdate.RollbackError(err); rerr != nil {
			return committed{}, fmt.Errorf("replace %s: %v; the previous file could not be put back either and is at %s: %v",
				p.target, err, backup, rerr)
		}
	}
	return committed{}, fmt.Errorf("replace %s: %w", p.target, err)
}

// backupPath is where the replaced file goes until both programs are in
// place. A backup left by an earlier update is removed first; when it is
// still running (Windows refuses to delete a running program), this one
// gets its own name beside it.
func backupPath(target string) string {
	backup := target + ".old"
	if err := os.Remove(backup); err == nil || errors.Is(err, fs.ErrNotExist) {
		return backup
	}
	return fmt.Sprintf("%s.%d.old", target, time.Now().UnixNano())
}

func removeStaleBackups(target string) {
	stale, _ := filepath.Glob(target + ".*.old")
	for _, s := range stale {
		_ = os.Remove(s)
	}
}

func rollback(done []committed) error {
	var errs []error
	for i := len(done) - 1; i >= 0; i-- {
		c := done[i]
		var err error
		if c.backup == "" {
			err = os.Remove(c.target)
		} else {
			err = os.Rename(c.backup, c.target)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", c.target, err))
		}
	}
	return errors.Join(errs...)
}

func humanBytes(n int64) string {
	const unit = 1024
	switch {
	case n <= 0:
		return "size unknown"
	case n < unit:
		return fmt.Sprintf("%d B", n)
	case n < unit*unit:
		return fmt.Sprintf("%.1f KB", float64(n)/unit)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(unit*unit))
	}
}

// --- The CLI command ---------------------------------------------------------

// runUpdate is Update; a variable so tests of the command can point it at a
// fake release.
var runUpdate = Update

// NewUpdateCommand returns the "update" command. It installs a release into
// the directory of the running program -- both compa-kernel and compa, from
// the same archive -- whichever of the two binaryName is.
func NewUpdateCommand(binaryName string) *cobra.Command {
	var version, releaseURL string
	var allowDowngrade bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update compa and compa-kernel from the latest GitHub release",
		Long: fmt.Sprintf(`Installs a release of Compa from https://github.com/%s/%s/releases
into the directory holding %s. Both programs, compa and compa-kernel, come
from the same archive for this OS and architecture, verified against the
release's SHA256SUMS. Restart Compa afterwards to use the new version.`, Owner, Repo, binaryName),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tag, err := NormalizeTag(version)
			if err != nil {
				return err
			}
			fromURL, err := TagFromURL(releaseURL)
			if err != nil {
				return err
			}
			if tag != "" && releaseURL != "" && fromURL != tag {
				return errors.New("--version and --url name different releases")
			}
			if tag == "" {
				tag = fromURL
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			ctx, stop := signal.NotifyContext(ctx, os.Interrupt)
			defer stop()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Current version: %s\n", config.FormatVersion())
			res, err := runUpdate(ctx, Options{Tag: tag, Log: out, AllowDowngrade: allowDowngrade})
			if errors.Is(err, errDowngrade) {
				return fmt.Errorf("%w (--allow-downgrade)", err)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "Installed Compa %s in %s. Restart Compa to use the new version.\n", res.Tag, res.Dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&version, "version", "", "Release to install, such as v1.2.3 (default: the latest release)")
	cmd.Flags().StringVarP(&releaseURL, "url", "u", "",
		"Release page to install, such as https://github.com/xibodev/compa/releases/tag/v1.2.3")
	cmd.Flags().BoolVar(&allowDowngrade, "allow-downgrade", false,
		"Install the release even when it is older than the running version")
	return cmd
}
