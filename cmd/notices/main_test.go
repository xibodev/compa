package main

import (
	"bytes"
	"errors"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/fake.golden")

// fakeGo is a toolchain whose programs and packages a test makes up.
type fakeGo struct {
	goVersion, goroot string
	infos             map[string]*debug.BuildInfo
	lists             map[string][]listedPackage
	// listed records the environment and tags each list call got.
	listed map[string]string
}

func (f *fakeGo) env(names ...string) ([]string, error) {
	var values []string
	for _, name := range names {
		switch name {
		case "GOVERSION":
			values = append(values, f.goVersion)
		case "GOROOT":
			values = append(values, f.goroot)
		default:
			return nil, errors.New("unexpected go env " + name)
		}
	}
	return values, nil
}

func (f *fakeGo) buildInfo(program string) (*debug.BuildInfo, error) {
	info, ok := f.infos[program]
	if !ok {
		return nil, errors.New("not a Go program")
	}
	return info, nil
}

func (f *fakeGo) list(pkg string, env []string, tags string) ([]listedPackage, error) {
	if f.listed == nil {
		f.listed = map[string]string{}
	}
	f.listed[pkg] = strings.Join(env, " ") + " -tags=" + tags
	return f.lists[pkg], nil
}

// writeFiles writes text files under root, by slash-separated path.
func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, text := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fakeBuild makes up two programs, compa and compa-kernel, built for
// windows/amd64 from modules in a temporary module cache:
//   - example.com/a, in both, with a LICENSE in CRLF;
//   - example.com/b, in compa-kernel, with two licenses at its root, a
//     third in the directory of a package compiled in, and a fourth in a
//     directory none is compiled from;
//   - example.com/c, in compa, replaced by example.com/c-fork, whose
//     LICENSES/MIT.txt is example.com/a's text;
//   - example.com/BigCo/d, in compa-kernel, with an upper-case path, whose
//     only package compiled in sits in a directory below its license.
func fakeBuild(t *testing.T) *fakeGo {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"goroot/LICENSE":                    "Go license\n",
		"goroot/PATENTS":                    "Go patents\n",
		"a/LICENSE":                         "MIT License\r\n\r\nCopyright (c) A\r\n",
		"a/sub/sub.go":                      "package sub\n",
		"b/LICENSE-MIT":                     "MIT License\n\nCopyright (c) B\n",
		"b/LICENSE-APACHE":                  "Apache License\n",
		"b/license.go":                      "package b\n",
		"b/LICENSE.minisig":                 "untrusted comment: signature\n",
		"b/third_party/xxhash/LICENSE.txt":  "xxhash license\n",
		"b/unused/LICENSE":                  "never compiled in\n",
		"fork/LICENSES/MIT.txt":             "MIT License\n\nCopyright (c) A\n",
		"big/COPYING":                       "BigCo license\n",
		"compa/web/main.go":                 "package main\n",
		"compa/kernel/main.go":              "package main\n",
		"compa/kernel/LICENSE":              "the main module's own license\n",
		"compa/web/NOTICE":                  "the main module's own notice\n",
		"compa/LICENSE":                     "the main module's own license\n",
		"b/third_party/xxhash/xxhash.go":    "package xxhash\n",
		"big/inner/inner.go":                "package inner\n",
		"fork/c.go":                         "package c\n",
		"a/a.go":                            "package a\n",
		"b/b.go":                            "package b\n",
		"b/unused/unused.go":                "package unused\n",
		"a/sub/README.md":                   "not a license\n",
		"b/third_party/xxhash/COPYRIGHT.md": "Copyright (c) xxhash authors\n",
	})
	dir := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }

	modA := &listedModule{Path: "example.com/a", Version: "v1.0.0", Dir: dir("a")}
	modB := &listedModule{Path: "example.com/b", Version: "v1.2.0", Dir: dir("b")}
	modC := &listedModule{Path: "example.com/c", Version: "v1.0.0", Dir: dir("fork"),
		Replace: &listedModule{Path: "example.com/c-fork", Version: "v0.0.0-20260101000000-abcdef123456", Dir: dir("fork")}}
	modD := &listedModule{Path: "example.com/BigCo/d", Version: "v2.0.0+incompatible", Dir: dir("big")}
	mainModule := &listedModule{Path: "example.com/compa", Main: true, Dir: dir("compa")}

	dep := func(m *listedModule) *debug.Module {
		d := &debug.Module{Path: m.Path, Version: m.Version}
		if m.Replace != nil {
			d.Replace = &debug.Module{Path: m.Replace.Path, Version: m.Replace.Version}
		}
		return d
	}
	settings := []debug.BuildSetting{
		{Key: "-ldflags", Value: "-s -w"},
		{Key: "-tags", Value: "goolm,stdjson"},
		{Key: "-trimpath", Value: "true"},
		{Key: "CGO_ENABLED", Value: "0"},
		{Key: "GOARCH", Value: "amd64"},
		{Key: "GOOS", Value: "windows"},
		{Key: "GOAMD64", Value: "v1"},
		{Key: "vcs.revision", Value: "abc"},
	}
	return &fakeGo{
		goVersion: "go1.26.6",
		goroot:    dir("goroot"),
		infos: map[string]*debug.BuildInfo{
			"out/compa.exe": {
				GoVersion: "go1.26.6", Path: "example.com/compa/web", Main: debug.Module{Path: "example.com/compa"},
				Deps: []*debug.Module{dep(modA), dep(modC)}, Settings: settings,
			},
			"out/compa-kernel.exe": {
				GoVersion: "go1.26.6", Path: "example.com/compa/kernel", Main: debug.Module{Path: "example.com/compa"},
				Deps: []*debug.Module{dep(modA), dep(modB), dep(modD)}, Settings: settings,
			},
		},
		lists: map[string][]listedPackage{
			"example.com/compa/web": {
				{ImportPath: "fmt", Dir: dir("goroot/src/fmt")},
				{ImportPath: "example.com/a", Dir: dir("a"), Module: modA},
				{ImportPath: "example.com/c", Dir: dir("fork"), Module: modC},
				{ImportPath: "example.com/compa/web", Dir: dir("compa/web"), Module: mainModule},
			},
			"example.com/compa/kernel": {
				{ImportPath: "example.com/a/sub", Dir: dir("a/sub"), Module: modA},
				{ImportPath: "example.com/b", Dir: dir("b"), Module: modB},
				{ImportPath: "example.com/b/third_party/xxhash", Dir: dir("b/third_party/xxhash"), Module: modB},
				{ImportPath: "example.com/BigCo/d/inner", Dir: dir("big/inner"), Module: modD},
				{ImportPath: "example.com/compa/kernel", Dir: dir("compa/kernel"), Module: mainModule},
			},
		},
	}
}

// npmFiles writes the two license files of a web UI build: Vite's and the
// stylesheets'. A package whose text is example.com/a's is in the first; one
// without a text is in neither; two are in both, with a text in only one, the
// first or the second.
func npmFiles(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"license.json": `[
  {"name": "@scope/pkg", "version": "1.0.0", "identifier": "MIT", "text": "MIT License\n\nCopyright (c) A"},
  {"name": "bare", "version": "2.0.0", "identifier": "ISC"},
  {"name": "early", "version": "3.0.0", "identifier": "BSD-3-Clause", "text": "BSD license of early"},
  {"name": "silent", "version": "0.1.0"}
]`,
		"css-licenses.json": `[
  {"name": "@fontsource-variable/inter", "version": "5.3.0", "identifier": "OFL-1.1", "text": "Inter, under the OFL"},
  {"name": "bare", "version": "2.0.0", "identifier": "ISC", "text": "ISC License"},
  {"name": "early", "version": "3.0.0", "identifier": "BSD-3-Clause"}
]`,
	})
	return []string{filepath.Join(root, "license.json"), filepath.Join(root, "css-licenses.json")}
}

func TestNoticesOfAFakeBuild(t *testing.T) {
	f := fakeBuild(t)
	npm := npmFiles(t)
	var out bytes.Buffer
	err := run([]string{"-npm", npm[0], "-npm", npm[1], "out/compa.exe", "out/compa-kernel.exe"}, &out, f)
	if err != nil {
		t.Fatal(err)
	}

	golden := filepath.Join("testdata", "fake.golden")
	if *update {
		if err := os.WriteFile(golden, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != string(want) {
		gotLines, wantLines := strings.Split(got, "\n"), strings.Split(string(want), "\n")
		for i := range max(len(gotLines), len(wantLines)) {
			var g, w string
			if i < len(gotLines) {
				g = gotLines[i]
			}
			if i < len(wantLines) {
				w = wantLines[i]
			}
			if g != w {
				t.Fatalf("line %d is\n\t%q\nwant\n\t%q\n(go test ./cmd/notices -update rewrites %s)", i+1, g, w, golden)
			}
		}
	}

	// go list ran with the setting each program was built with.
	for _, pkg := range []string{"example.com/compa/web", "example.com/compa/kernel"} {
		if got, want := f.listed[pkg], "CGO_ENABLED=0 GOARCH=amd64 GOOS=windows GOAMD64=v1 -tags=goolm,stdjson"; got != want {
			t.Errorf("go list %s ran with %q, want %q", pkg, got, want)
		}
	}
}

func TestWritesTheOutputFile(t *testing.T) {
	output := filepath.Join(t.TempDir(), "THIRD_PARTY_NOTICES")
	var stdout bytes.Buffer
	if err := run([]string{"-o", output, "out/compa.exe"}, &stdout, fakeBuild(t)); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(text), "THIRD-PARTY SOFTWARE NOTICES\n") || stdout.Len() != 0 {
		t.Fatalf("file %q, stdout %q", text, stdout.String())
	}
}

func TestFailures(t *testing.T) {
	cases := []struct {
		name   string
		args   []string
		change func(t *testing.T, f *fakeGo)
		want   string
	}{
		{"no program", nil, nil, "usage: notices"},
		{"not a Go program", []string{"out/other"}, nil, "out/other: not a Go program"},
		{
			"built with another Go", []string{"out/compa.exe"},
			func(_ *testing.T, f *fakeGo) { f.goVersion = "go1.27.0" },
			"out/compa.exe was built with go1.26.6, but the go here is go1.27.0",
		},
		{
			"a module the program records is not listed", []string{"out/compa.exe"},
			func(_ *testing.T, f *fakeGo) {
				info := f.infos["out/compa.exe"]
				info.Deps = append(info.Deps, &debug.Module{Path: "example.com/gone", Version: "v1.0.0"})
			},
			"go list does not reproduce the build: recorded only [example.com/gone@v1.0.0], listed only []",
		},
		{
			"a module without a license", []string{"out/compa-kernel.exe"},
			func(t *testing.T, f *fakeGo) { unlicense(t, f) },
			"no license file in these modules: example.com/BigCo/d@v2.0.0+incompatible",
		},
		{
			"an exception no module needs", []string{"-unlicensed", "example.com/BigCo/d", "out/compa-kernel.exe"},
			nil,
			"-unlicensed example.com/BigCo/d: no module of that path lacks a license file; drop the exception",
		},
		{
			"a package outside its module", []string{"out/compa.exe"},
			func(_ *testing.T, f *fakeGo) {
				f.lists["example.com/compa/web"][1].Dir = f.goroot
			},
			"outside its module",
		},
		{
			"a local directory replacement", []string{"out/compa.exe"},
			func(_ *testing.T, f *fakeGo) {
				f.lists["example.com/compa/web"][2].Module.Replace.Version = ""
			},
			"comes from the directory example.com/c-fork, which has no version",
		},
		{
			"an npm package without a version", []string{"-npm", "", "out/compa.exe"},
			nil,
			"a package without a name or version",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := fakeBuild(t)
			if tc.change != nil {
				tc.change(t, f)
			}
			args := slices.Clone(tc.args)
			if i := slices.Index(args, "-npm"); i >= 0 && args[i+1] == "" {
				args[i+1] = filepath.Join(t.TempDir(), "license.json")
				writeFiles(t, filepath.Dir(args[i+1]), map[string]string{"license.json": `[{"name": "x"}]`})
			}
			err := run(args, &bytes.Buffer{}, f)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("run(%q) = %v, want an error containing %q", args, err, tc.want)
			}
		})
	}
}

// unlicense removes example.com/BigCo/d's only license file.
func unlicense(t *testing.T, f *fakeGo) {
	t.Helper()
	dir := f.lists["example.com/compa/kernel"][3].Module.Dir
	if err := os.Remove(filepath.Join(dir, "COPYING")); err != nil {
		t.Fatal(err)
	}
}

// A known exception is listed as having no license instead of failing.
func TestUnlicensedException(t *testing.T) {
	f := fakeBuild(t)
	unlicense(t, f)
	var out bytes.Buffer
	if err := run([]string{"-unlicensed", "example.com/BigCo/d", "out/compa-kernel.exe"}, &out, f); err != nil {
		t.Fatal(err)
	}
	want := "example.com/BigCo/d v2.0.0+incompatible\n  in: compa-kernel\n  license: none; the module has no license file\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("notices lack %q:\n%s", want, out.String())
	}
}

func TestIsLicenseFile(t *testing.T) {
	for name, want := range map[string]bool{
		"LICENSE": true, "LICENSE.txt": true, "LICENSE.md": true, "license": true, "Licence": true,
		"LICENSE-MIT": true, "LICENSE_APACHE": true, "COPYING": true, "COPYING.LESSER": true,
		"NOTICE": true, "NOTICE.md": true, "COPYRIGHT": true, "PATENTS": true, "UNLICENSE": true,
		"license.go": false, "notice_test.go": false, "LICENSE.minisig": false, "LICENSE.sig": false,
		"license.json": false, "README.md": false, "licensing_test.go": false, "go.mod": false,
	} {
		if got := isLicenseFile(name); got != want {
			t.Errorf("isLicenseFile(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestSourceURLs(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{proxyURL("github.com/BurntSushi/toml", "v1.4.0"), "https://proxy.golang.org/github.com/!burnt!sushi/toml/@v/v1.4.0.zip"},
		{proxyURL("example.com/m", "v1.0.0-RC1"), "https://proxy.golang.org/example.com/m/@v/v1.0.0-!r!c1.zip"},
		{npmURL("@babel/runtime", "7.29.7"), "https://registry.npmjs.org/@babel/runtime/-/runtime-7.29.7.tgz"},
		{npmURL("react", "19.2.5"), "https://registry.npmjs.org/react/-/react-19.2.5.tgz"},
	} {
		if tc.got != tc.want {
			t.Errorf("got %s, want %s", tc.got, tc.want)
		}
	}
}

// The go command itself: notices of a program with no module, notices
// itself, built here.
func TestTheGoCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a program")
	}
	program := filepath.Join(t.TempDir(), "notices")
	if runtime.GOOS == "windows" {
		program += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", program, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	n, err := collect([]string{program}, nil, nil, goCommand{})
	if err != nil {
		t.Fatal(err)
	}
	if len(n.modules) != 0 || len(n.programs) != 1 {
		t.Fatalf("modules %v, programs %v", n.modules, n.programs)
	}
	if p := n.programs[0]; p.name != "notices" || p.goos != runtime.GOOS || p.goarch != runtime.GOARCH {
		t.Fatalf("program %+v", p)
	}
	if n.goVersion != runtime.Version() || len(n.goLicenses) == 0 || n.goLicenses[0].name != "LICENSE" ||
		!strings.Contains(n.goLicenses[0].text, "The Go Authors") {
		t.Fatalf("Go %s, licenses %v", n.goVersion, n.goLicenses)
	}
}
