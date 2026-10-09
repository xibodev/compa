// Command notices writes the THIRD_PARTY_NOTICES file of a release archive:
// the license texts of the third-party code compiled into the programs given
// (Go modules and the Go standard library) and bundled into compa's web UI
// (npm packages), with where to get each one's source.
//
// Run it from the repository root, with the Go that built the programs:
//
//	go run ./cmd/notices -o THIRD_PARTY_NOTICES \
//		-npm web/backend/dist/.vite/license.json \
//		-npm web/backend/dist/.vite/css-licenses.json \
//		out/compa out/compa-kernel
//
// For each program it reads the build information Go records in it, lists
// the packages compiled in with go list under the same GOOS, GOARCH, cgo
// setting and build tags, and checks that they come from exactly the modules
// the program records. A module's license files are those in the directory
// of each of its packages compiled in, and in the directories above it up to
// the module's root. It fails when a module has none.
package main

import (
	"bytes"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"slices"
	"strings"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, goCommand{}); err != nil {
		fmt.Fprintln(os.Stderr, "notices:", err)
		os.Exit(1)
	}
}

// stringsFlag collects the values of a flag given more than once.
type stringsFlag []string

func (f *stringsFlag) String() string { return strings.Join(*f, ",") }

func (f *stringsFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func run(args []string, stdout io.Writer, goTool toolchain) error {
	flags := flag.NewFlagSet("notices", flag.ContinueOnError)
	output := flags.String("o", "", "write the notices to `file` instead of stdout")
	var npmFiles, unlicensed stringsFlag
	flags.Var(&npmFiles, "npm", "an npm license `file` written by the web UI build; repeatable")
	flags.Var(&unlicensed, "unlicensed", "a `module` known to have no license file, listed as such instead of failing; repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() == 0 {
		return errors.New("usage: notices [-o file] [-npm file]... program...")
	}

	n, err := collect(flags.Args(), npmFiles, unlicensed, goTool)
	if err != nil {
		return err
	}
	var text bytes.Buffer
	render(&text, n)
	if *output == "" {
		_, err = stdout.Write(text.Bytes())
		return err
	}
	return os.WriteFile(*output, text.Bytes(), 0o644)
}

// toolchain is the Go toolchain notices asks; tests replace it.
type toolchain interface {
	// env returns the values of the go env variables named.
	env(names ...string) ([]string, error)
	// buildInfo returns the build information Go recorded in a program.
	buildInfo(program string) (*debug.BuildInfo, error)
	// list returns pkg and the packages it depends on, as go list -deps
	// does with env added to the environment and the build tags given.
	list(pkg string, env []string, tags string) ([]listedPackage, error)
}

// listedPackage is what notices reads of a package go list prints.
type listedPackage struct {
	ImportPath string
	Dir        string
	// Module is nil for a package of the standard library.
	Module *listedModule
}

type listedModule struct {
	Path    string
	Version string
	Dir     string
	Main    bool
	Replace *listedModule
}

type goCommand struct{}

func (goCommand) env(names ...string) ([]string, error) {
	out, err := exec.Command("go", append([]string{"env"}, names...)...).Output()
	if err != nil {
		return nil, fmt.Errorf("go env: %w", commandError(err))
	}
	values := strings.Split(strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n"), "\n")
	if len(values) != len(names) {
		return nil, fmt.Errorf("go env printed %d values for %d variables", len(values), len(names))
	}
	return values, nil
}

func (goCommand) buildInfo(program string) (*debug.BuildInfo, error) {
	return buildinfo.ReadFile(program)
}

func (goCommand) list(pkg string, env []string, tags string) ([]listedPackage, error) {
	cmd := exec.Command("go", "list", "-deps", "-tags="+tags, "-json=ImportPath,Dir,Module", pkg)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list %s: %w", pkg, commandError(err))
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var p listedPackage
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return pkgs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("go list %s: %w", pkg, err)
		}
		pkgs = append(pkgs, p)
	}
}

// commandError adds what a failed command printed on stderr to its error.
func commandError(err error) error {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return fmt.Errorf("%w: %s", err, bytes.TrimSpace(exit.Stderr))
	}
	return err
}

// notices is everything THIRD_PARTY_NOTICES lists.
type notices struct {
	programs  []program
	goVersion string
	// goLicenses are the license files of the Go distribution.
	goLicenses []licenseFile
	modules    []*module
	npm        []npmPackage
}

type program struct {
	name, goos, goarch, cgo, tags string
}

// module is a Go module some of whose packages a program includes.
type module struct {
	path, version string
	// replaces is the module path go.mod replaced with this one, if any.
	replaces string
	// programs are the names of the programs that include the module.
	programs []string
	licenses []licenseFile
}

type licenseFile struct {
	// name is the file's path from its module's root, with slashes.
	name string
	text string
}

// npmPackage is an entry of the license files the web UI build writes: the
// shape of Vite's build.license JSON.
type npmPackage struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Identifier string `json:"identifier,omitempty"`
	Text       string `json:"text,omitempty"`
}

func collect(programs, npmFiles, unlicensed []string, goTool toolchain) (*notices, error) {
	values, err := goTool.env("GOVERSION", "GOROOT")
	if err != nil {
		return nil, err
	}
	n := &notices{goVersion: values[0]}
	goroot := values[1]

	modules := map[string]*module{}
	for _, path := range programs {
		info, err := goTool.buildInfo(path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		if info.GoVersion != n.goVersion {
			return nil, fmt.Errorf("%s was built with %s, but the go here is %s: run notices with the Go that built it",
				path, info.GoVersion, n.goVersion)
		}
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		p := program{
			name:   strings.TrimSuffix(filepath.Base(path), ".exe"),
			goos:   settings["GOOS"],
			goarch: settings["GOARCH"],
			cgo:    settings["CGO_ENABLED"],
			tags:   settings["-tags"],
		}
		n.programs = append(n.programs, p)

		pkgs, err := goTool.list(info.Path, buildEnv(info.Settings), p.tags)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		listed := map[string]bool{}
		for _, pkg := range pkgs {
			if pkg.Module == nil || pkg.Module.Main {
				continue
			}
			source := pkg.Module
			if source.Replace != nil {
				source = source.Replace
			}
			if source.Version == "" {
				return nil, fmt.Errorf("%s: package %s comes from the directory %s, which has no version to say where its source is",
					path, pkg.ImportPath, source.Path)
			}
			key := source.Path + "@" + source.Version
			listed[key] = true
			m := modules[key]
			if m == nil {
				m = &module{path: source.Path, version: source.Version}
				if pkg.Module.Replace != nil {
					m.replaces = pkg.Module.Path
				}
				modules[key] = m
			}
			if !slices.Contains(m.programs, p.name) {
				m.programs = append(m.programs, p.name)
			}
			files, err := licenseFiles(pkg.Dir, source.Dir)
			if err != nil {
				return nil, fmt.Errorf("%s: package %s: %w", path, pkg.ImportPath, err)
			}
			for _, f := range files {
				if !slices.ContainsFunc(m.licenses, func(l licenseFile) bool { return l.name == f.name }) {
					m.licenses = append(m.licenses, f)
				}
			}
		}
		if err := sameModules(info, listed); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}

	// A module without a license file fails, unless it is a known exception;
	// an exception no module needs fails too, so none outlives its module.
	var missing []string
	needed := map[string]bool{}
	for _, m := range modules {
		if len(m.licenses) == 0 {
			if slices.Contains(unlicensed, m.path) {
				needed[m.path] = true
			} else {
				missing = append(missing, m.path+"@"+m.version)
			}
		}
		slices.SortFunc(m.licenses, func(a, b licenseFile) int { return strings.Compare(a.name, b.name) })
		n.modules = append(n.modules, m)
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return nil, fmt.Errorf("no license file in these modules: %s", strings.Join(missing, ", "))
	}
	for _, path := range unlicensed {
		if !needed[path] {
			return nil, fmt.Errorf("-unlicensed %s: no module of that path lacks a license file; drop the exception", path)
		}
	}
	slices.SortFunc(n.modules, func(a, b *module) int {
		return strings.Compare(a.path+"@"+a.version, b.path+"@"+b.version)
	})

	for _, name := range []string{"LICENSE", "PATENTS"} {
		text, err := os.ReadFile(filepath.Join(goroot, name))
		if errors.Is(err, os.ErrNotExist) && name != "LICENSE" {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("the Go distribution's %s: %w", name, err)
		}
		n.goLicenses = append(n.goLicenses, licenseFile{name: name, text: normalize(text)})
	}

	if n.npm, err = readNPM(npmFiles); err != nil {
		return nil, err
	}
	return n, nil
}

// buildEnv is the environment that selects the files a program was built
// from: its GOOS, GOARCH, cgo setting and GOAMD64-like variables.
func buildEnv(settings []debug.BuildSetting) []string {
	var env []string
	for _, s := range settings {
		if s.Key == "CGO_ENABLED" || (strings.HasPrefix(s.Key, "GO") && s.Key == strings.ToUpper(s.Key)) {
			env = append(env, s.Key+"="+s.Value)
		}
	}
	return env
}

// sameModules checks that the packages listed come from exactly the
// modules the program records, so the list is the program's.
func sameModules(info *debug.BuildInfo, listed map[string]bool) error {
	recorded := map[string]bool{}
	for _, dep := range info.Deps {
		source := dep
		if dep.Replace != nil {
			source = dep.Replace
		}
		recorded[source.Path+"@"+source.Version] = true
	}
	var onlyRecorded, onlyListed []string
	for key := range recorded {
		if !listed[key] {
			onlyRecorded = append(onlyRecorded, key)
		}
	}
	for key := range listed {
		if !recorded[key] {
			onlyListed = append(onlyListed, key)
		}
	}
	if len(onlyRecorded) == 0 && len(onlyListed) == 0 {
		return nil
	}
	slices.Sort(onlyRecorded)
	slices.Sort(onlyListed)
	return fmt.Errorf("go list does not reproduce the build: recorded only %v, listed only %v", onlyRecorded, onlyListed)
}

// licenseName matches the names of license files, with notLicenseExt ruling
// out source files such as license.go and signatures such as
// LICENSE.minisig.
var (
	licenseName   = regexp.MustCompile(`(?i)^((un)?licen[cs]e|copying|copyright|notice|patents)`)
	notLicenseExt = map[string]bool{
		".go": true, ".s": true, ".c": true, ".h": true, ".js": true, ".mjs": true, ".cjs": true,
		".ts": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".html": true,
		".css": true, ".sh": true, ".py": true, ".proto": true, ".tmpl": true, ".tpl": true,
		".minisig": true, ".sig": true, ".asc": true,
	}
)

func isLicenseFile(name string) bool {
	return licenseName.MatchString(name) && !notLicenseExt[strings.ToLower(filepath.Ext(name))]
}

// licenseFiles reads the license files in dir and the directories above it
// up to root, the root of dir's module, including the files of a LICENSES
// directory.
func licenseFiles(dir, root string) ([]licenseFile, error) {
	dir, root = filepath.Clean(dir), filepath.Clean(root)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("directory %s is outside its module's %s", dir, root)
	}
	// dir and the directories above it up to root: one for each element of
	// rel, and root.
	levels := 1
	if rel != "." {
		levels += strings.Count(rel, string(filepath.Separator)) + 1
	}
	var files []licenseFile
	read := func(path string) error {
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, licenseFile{name: filepath.ToSlash(name), text: normalize(text)})
		return nil
	}
	for range levels {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			switch {
			case e.Type().IsRegular() && isLicenseFile(e.Name()):
				if err := read(path); err != nil {
					return nil, err
				}
			case e.IsDir() && strings.EqualFold(e.Name(), "LICENSES"):
				texts, err := os.ReadDir(path)
				if err != nil {
					return nil, err
				}
				for _, t := range texts {
					if t.Type().IsRegular() {
						if err := read(filepath.Join(path, t.Name())); err != nil {
							return nil, err
						}
					}
				}
			}
		}
		dir = filepath.Dir(dir)
	}
	return files, nil
}

// normalize gives a text Unix line endings and one final newline.
func normalize(text []byte) string {
	s := strings.ReplaceAll(string(text), "\r\n", "\n")
	return strings.TrimRight(s, " \t\n") + "\n"
}

func readNPM(files []string) ([]npmPackage, error) {
	byKey := map[string]npmPackage{}
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var pkgs []npmPackage
		if err := json.Unmarshal(data, &pkgs); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		for _, p := range pkgs {
			if p.Name == "" || p.Version == "" {
				return nil, fmt.Errorf("%s: a package without a name or version: %+v", file, p)
			}
			key := p.Name + "@" + p.Version
			if prev, ok := byKey[key]; ok && prev.Text != "" {
				continue
			}
			if p.Text != "" {
				p.Text = normalize([]byte(p.Text))
			}
			byKey[key] = p
		}
	}
	pkgs := make([]npmPackage, 0, len(byKey))
	for _, p := range byKey {
		pkgs = append(pkgs, p)
	}
	slices.SortFunc(pkgs, func(a, b npmPackage) int {
		return strings.Compare(a.Name+"@"+a.Version, b.Name+"@"+b.Version)
	})
	return pkgs, nil
}

// proxyURL is where the Go module proxy serves a module version's source.
func proxyURL(path, version string) string {
	return "https://proxy.golang.org/" + escapeModule(path) + "/@v/" + escapeModule(version) + ".zip"
}

// escapeModule writes an upper-case letter as "!" and its lower case, as
// module proxies spell module paths and versions.
func escapeModule(s string) string {
	var b strings.Builder
	for _, r := range s {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// npmURL is where the npm registry serves a package version.
func npmURL(name, version string) string {
	base := name
	if i := strings.LastIndex(name, "/"); i >= 0 {
		base = name[i+1:]
	}
	return "https://registry.npmjs.org/" + name + "/-/" + base + "-" + version + ".tgz"
}

// textIndex numbers license texts, once each, in the order they are first
// referred to.
type textIndex struct {
	numbers map[string]int
	texts   []string
}

func (x *textIndex) ref(text string) string {
	if x.numbers == nil {
		x.numbers = map[string]int{}
	}
	n, ok := x.numbers[text]
	if !ok {
		x.texts = append(x.texts, text)
		n = len(x.texts)
		x.numbers[text] = n
	}
	return fmt.Sprintf("[%d]", n)
}

func (x *textIndex) refs(files []licenseFile) string {
	var parts []string
	for _, f := range files {
		parts = append(parts, x.ref(f.text)+" "+f.name)
	}
	return strings.Join(parts, ", ")
}

const rule = "==============================================================================="

func render(w io.Writer, n *notices) {
	var texts textIndex
	names := make([]string, 0, len(n.programs))
	for _, p := range n.programs {
		names = append(names, p.name)
	}

	fmt.Fprint(w, `THIRD-PARTY SOFTWARE NOTICES

These programs include the third-party software listed below:

`)
	for _, p := range n.programs {
		tags := p.tags
		if tags == "" {
			tags = "none"
		}
		fmt.Fprintf(w, "  %s, for %s/%s, built with %s, cgo %s and the build tags %s\n",
			p.name, p.goos, p.goarch, n.goVersion, onOff(p.cgo), tags)
	}
	fmt.Fprint(w, `
Each entry names the programs that include it, the license texts that apply
to it, numbered and printed in full at the end, and where to get its source.

`)

	fmt.Fprintf(w, "THE GO STANDARD LIBRARY AND RUNTIME\n\n")
	fmt.Fprintf(w, "%s\n  in: %s\n  license: %s\n  source: https://go.dev/dl/%s.src.tar.gz\n\n",
		n.goVersion, strings.Join(names, ", "), texts.refs(n.goLicenses), n.goVersion)

	fmt.Fprintf(w, "GO MODULES\n\n")
	if len(n.modules) == 0 {
		fmt.Fprintf(w, "None.\n\n")
	}
	for _, m := range n.modules {
		fmt.Fprintf(w, "%s %s\n", m.path, m.version)
		if m.replaces != "" {
			fmt.Fprintf(w, "  replaces: %s\n", m.replaces)
		}
		license := "none; the module has no license file"
		if len(m.licenses) > 0 {
			license = texts.refs(m.licenses)
		}
		fmt.Fprintf(w, "  in: %s\n  license: %s\n  source: %s\n\n",
			strings.Join(m.programs, ", "), license, proxyURL(m.path, m.version))
	}

	if len(n.npm) > 0 {
		fmt.Fprintf(w, "NPM PACKAGES IN COMPA'S WEB UI\n\n")
		for _, p := range n.npm {
			identifier := p.Identifier
			if identifier == "" {
				identifier = "no license named"
			}
			license := "none in the package"
			if p.Text != "" {
				license = texts.ref(p.Text)
			}
			fmt.Fprintf(w, "%s %s (%s)\n  license: %s\n  source: %s\n\n",
				p.Name, p.Version, identifier, license, npmURL(p.Name, p.Version))
		}
	}

	fmt.Fprintf(w, "LICENSE TEXTS\n")
	for i, text := range texts.texts {
		fmt.Fprintf(w, "\n%s\n[%d]\n%s\n\n%s", rule, i+1, rule, text)
	}
}

func onOff(cgo string) string {
	if cgo == "1" {
		return "on"
	}
	return "off"
}
