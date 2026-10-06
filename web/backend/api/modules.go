package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xibodev/compa/v2/internal/module"
	"github.com/xibodev/compa/v2/internal/moduletools"
	"github.com/xibodev/compa/v2/pkg/approval"
	"github.com/xibodev/compa/v2/pkg/config"
	"github.com/xibodev/compa/v2/pkg/modproto"
)

// Module lifecycle over HTTP.
//
// The cockpit shows what is installed, what each module can do, and what
// authority it declares -- and lets a person install, remove, and try a
// capability. Every decision that matters already belongs to internal/module;
// these handlers present those answers rather than re-deriving them.
//
// Two presentation rules are load-bearing rather than cosmetic:
//
//   - unknown cost renders as UNKNOWN, never as 0 or "free". Collapsing the two
//     is how an unpriced provider call slips past approval.
//   - a capability's declared effects are shown BEFORE it runs, so a person
//     approving a call sees what it claims it will do.
func (h *Handler) registerModuleRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/modules", h.handleModulesList)
	mux.HandleFunc("GET /api/modules/location", h.handleModulesLocation)
	mux.HandleFunc("POST /api/modules/install", h.handleModuleInstall)
	mux.HandleFunc("DELETE /api/modules/{id}", h.handleModuleRemove)
	mux.HandleFunc("POST /api/modules/{id}/enabled", h.handleModuleSetEnabled)
	mux.HandleFunc("POST /api/modules/{id}/invoke", h.handleModuleInvoke)
	mux.HandleFunc("GET /api/modules/artifact", h.handleArtifact)
}

// ModuleView is a descriptor flattened for the cockpit.
type ModuleView struct {
	Module       string                 `json:"module"`
	Name         string                 `json:"name"`
	Version      string                 `json:"version"`
	Binary       string                 `json:"binary"`
	Capabilities []CapabilityView       `json:"capabilities"`
	Overlays     int                    `json:"overlays"`
	Skills       int                    `json:"skills"`
	Permissions  PermissionsView        `json:"permissions"`
	Requirements []modproto.Requirement `json:"requirements"`
	Warnings     []string               `json:"warnings"`
	// HostWarnings are the host's own findings about this module -- a stale
	// digest, a cost claim that contradicts a declaration -- kept apart from
	// what the module said about itself, so a reader can tell who is
	// complaining and which they can act on.
	HostWarnings []string `json:"host_warnings"`
	// Enabled is false when the user has turned this module off. It stays
	// listed with its capabilities, because there has to be something to turn
	// back on.
	Enabled bool   `json:"enabled"`
	Error   string `json:"error,omitempty"`
	// Dir is the install directory name, carried so a module that could not
	// describe itself can still be REMOVED. Such a module has no module id --
	// the id comes from the descriptor it failed to produce -- so without this
	// the cockpit can report the breakage and offer no way to clear it.
	Dir string `json:"dir,omitempty"`
}

type CapabilityView struct {
	ID             string          `json:"id"`
	Title          string          `json:"title"`
	Summary        string          `json:"summary"`
	Local          bool            `json:"local"`
	Network        bool            `json:"network"`
	ExternalWrites bool            `json:"external_writes"`
	Provider       string          `json:"provider"`
	CostKnown      bool            `json:"cost_known"`
	RequestSchema  json.RawMessage `json:"request_schema,omitempty"`
	// ToolName is what the agent calls this capability in chat, so a person can
	// see the connection between a capability here and a tool the agent used.
	ToolName string `json:"tool_name"`
	// NeedsApproval is true when the approval policy (tools.approval) asks
	// before a run from this page, which then offers "Approve and run".
	NeedsApproval bool `json:"needs_approval"`
}

// PermissionsView is what a module DECLARED it may need. It is a request, never
// a grant: installing a module grants nothing, and the host authorizes per
// invocation.
type PermissionsView struct {
	FilesystemRead  []string     `json:"filesystem_read"`
	FilesystemWrite []string     `json:"filesystem_write"`
	Network         []string     `json:"network"`
	Credentials     []string     `json:"credentials"`
	PaidProviders   []string     `json:"paid_providers"`
	Publish         bool         `json:"publish"`
	Subprocess      []BinaryView `json:"subprocess"`
}

type BinaryView struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Resolved bool   `json:"resolved"`
}

func (h *Handler) handleModulesList(w http.ResponseWriter, r *http.Request) {
	views := []ModuleView{}
	// Read at most once, and only if a module declares a source root.
	configured := sync.OnceValues(h.configuredSourceRoots)
	// A policy that cannot be read refuses every run, saying why; the list
	// shows what the default policy asks.
	policy, err := h.moduleApprovalPolicy()
	if err != nil {
		policy = approval.DefaultPolicy()
	}
	for _, in := range moduletools.Discover(r.Context(), config.GetHome()) {
		if in.Err != nil {
			// A broken module is reported, never fatal: one bad install must
			// not hide the others.
			views = append(views, ModuleView{
				Binary: filepath.Base(in.Runner.Binary),
				Name:   filepath.Base(in.Runner.Binary),
				Error:  module.BoundText(in.Err.Error(), module.MaxErrorText),
				Dir:    brokenModuleDir(in.Runner.Binary),
			})
			continue
		}
		if len(moduletools.SourceRootNames(in.Descriptor)) > 0 {
			sources, err := configured()
			in.HostWarnings = append(in.HostWarnings, sourceRootHostWarnings(in.Descriptor, sources, err)...)
		}
		views = append(views, moduleView(in, policy))
	}
	writeJSON(w, http.StatusOK, views)
}

// brokenModuleDir is the install directory a broken module's card may offer
// to remove: the module's own directory, never the modules directory a bare
// binary sits in.
func brokenModuleDir(binary string) string {
	dir := filepath.Base(filepath.Dir(binary))
	if !module.ValidID(dir) || filepath.Base(binary) != module.BinaryName(dir) {
		return ""
	}
	return dir
}

// moduleApprovalPolicy is the owner's approval policy (tools.approval), read
// from the saved config on every request so a change applies at once.
func (h *Handler) moduleApprovalPolicy() (approval.Policy, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return approval.Policy{}, fmt.Errorf("could not read the approval policy from %s: %w", h.configPath, err)
	}
	return cfg.Tools.Approval, nil
}

// moduleWorkspace is the workspace module invocations and artefacts use: the
// one the config gives the agents, so the cockpit and the agent read and
// write the same folder.
func (h *Handler) moduleWorkspace() (string, error) {
	workspace := ""
	if cfg, err := config.LoadConfig(h.configPath); err == nil {
		workspace = cfg.WorkspacePath()
	}
	if strings.TrimSpace(workspace) == "" {
		workspace = filepath.Join(config.GetHome(), "workspace")
	}
	return filepath.Abs(workspace)
}

// pathModuleID is the module ID in the request path, refused before anything
// turns it into a path unless it is one the host could have installed.
// DELETE /api/modules/%2E%2E used to reach Remove with "..".
func pathModuleID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if err := module.CheckID(id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return "", false
	}
	return id, true
}

// configuredSourceRoots reads the operator's named source roots from the config
// this launcher was started with -- the same file the gateway is pointed at, so
// the Modules page grants what the agent grants.
func (h *Handler) configuredSourceRoots() (map[string]string, error) {
	cfg, err := config.LoadConfig(h.configPath)
	if err != nil {
		return nil, fmt.Errorf("could not read source roots from %s: %w", h.configPath, err)
	}
	return cfg.Modules.SourceRoots, nil
}

// sourceRootHostWarnings is the host's finding about the source roots a module
// declares: which will not be granted and what to configure, or -- when the
// config itself could not be read -- that none will.
func sourceRootHostWarnings(d *modproto.Descriptor, sources map[string]string, err error) []string {
	names := moduletools.SourceRootNames(d)
	if len(names) == 0 {
		return nil
	}
	if err != nil {
		return []string{fmt.Sprintf("declares source root(s) %s, but %v, so none are granted",
			strings.Join(names, ", "), err)}
	}
	return moduletools.SourceRootWarnings(d, sources)
}

func moduleView(in moduletools.Installed, policy approval.Policy) ModuleView {
	d := in.Descriptor

	caps := make([]CapabilityView, 0, len(d.Capabilities))
	for _, c := range d.Capabilities {
		cv := CapabilityView{
			ID: c.ID, Title: c.Title, Summary: c.Summary,
			Local: c.Effects.Local, Network: c.Effects.Network,
			ExternalWrites: c.Effects.ExternalWrites,
			Provider:       c.Effects.Provider,
			CostKnown:      c.Effects.CostKnown,
			ToolName:       moduletools.ToolName(d.Module, c.ID),
			// The decision invoke enforces, so the page never promises an
			// approval step the host skips.
			NeedsApproval: policy.Decide(moduletools.ApprovalTool(d, c), approval.OriginWeb).Action == approval.Ask,
		}
		if doc, ok := d.RequestSchemas[c.RequestSchema]; ok {
			cv.RequestSchema = doc
		}
		caps = append(caps, cv)
	}

	bins := []BinaryView{}
	if len(d.Permissions.Subprocess) > 0 {
		resolved, _ := module.ResolveBinaries(d.Permissions.Subprocess)
		for _, name := range d.Permissions.Subprocess {
			p, ok := resolved[name]
			bins = append(bins, BinaryView{Name: name, Path: p, Resolved: ok})
		}
	}

	warnings := in.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	hostWarnings := in.HostWarnings
	if hostWarnings == nil {
		hostWarnings = []string{}
	}

	return ModuleView{
		Module: d.Module, Name: d.Name, Version: d.Version,
		Binary:       filepath.Base(in.Runner.Binary),
		Capabilities: caps,
		Overlays:     len(d.AgentOverlays),
		Skills:       len(d.Skills),
		Permissions: PermissionsView{
			FilesystemRead:  append([]string{}, d.Permissions.FilesystemRead...),
			FilesystemWrite: append([]string{}, d.Permissions.FilesystemWrite...),
			Network:         append([]string{}, d.Permissions.Network...),
			Credentials:     append([]string{}, d.Permissions.Credentials...),
			PaidProviders:   append([]string{}, d.Permissions.PaidProviders...),
			Publish:         d.Permissions.Publish,
			Subprocess:      bins,
		},
		Requirements: append([]modproto.Requirement{}, d.Requirements...),
		Warnings:     warnings,
		Enabled:      !in.Disabled,
		HostWarnings: hostWarnings,
	}
}

// handleModulesLocation reports where installed modules live.
//
// The empty state told a first-time user to "drop an executable into the
// host's modules directory" without naming it, which is advice nobody can act
// on -- the path is derived from host state the browser cannot see.
//
// A separate endpoint rather than a field on the list: the list returns a bare
// array, and reshaping it into an object would break every existing reader for
// one string.
func (h *Handler) handleModulesLocation(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"modules_dir": moduletools.ModulesDir(config.GetHome()),
	})
}

type installRequest struct {
	// Path is a local path to a module binary. Installing is deliberately a
	// host action: the host validates, chooses the destination, and derives
	// identity from the descriptor, so a module never writes into host state
	// nor needs to know the host's layout.
	Path string `json:"path"`
}

func (h *Handler) handleModuleInstall(w http.ResponseWriter, r *http.Request) {
	var req installRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a path to a module binary is required"})
		return
	}

	id, err := module.Install(r.Context(), config.GetHome(), req.Path)
	if err != nil {
		// Refusal reasons are shown verbatim: "this binary does not speak the
		// protocol" is the answer a person needs, not a generic failure.
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"module": id})
}

func (h *Handler) handleModuleRemove(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModuleID(w, r)
	if !ok {
		return
	}
	if err := module.Remove(config.GetHome(), id); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"removed": id})
}

// handleModuleSetEnabled turns an installed module off or on WITHOUT removing
// it.
//
// Installing a module used to mean enabling it, with no middle state: every
// capability of every installed module was registered as an agent tool
// unconditionally. On this machine that was 13 of 33 tools -- 39% of the
// agent's tool budget -- spent on modules a user may not be using in this
// session. Removing the module to reclaim that also discards its state and its
// declared content, which is too big a hammer.
func (h *Handler) handleModuleSetEnabled(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModuleID(w, r)
	if !ok {
		return
	}

	var body struct {
		// The DESIRED state, not a toggle: a toggle races with whatever the
		// page last rendered, and two clicks would disagree about the result.
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "body must be {\"enabled\": true|false}"})
		return
	}

	home := config.GetHome()
	for _, in := range moduletools.Discover(r.Context(), home) {
		if in.Descriptor == nil || in.Descriptor.Module != id {
			continue
		}
		dir, ok := moduletools.ModuleDir(home, in.Runner.Binary)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "this module is installed as a bare binary in the shared" +
					" modules directory, so it has no directory of its own to" +
					" disable; reinstall it with modules-add to enable this"})
			return
		}
		if err := moduletools.SetDisabled(dir, !body.Enabled); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"module": id, "enabled": body.Enabled})
		return
	}
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": fmt.Sprintf("module %q is not installed", id)})
}

type invokeRequest struct {
	Capability string          `json:"capability"`
	Input      json.RawMessage `json:"input"`
	// Approved records that a person clicked "Approve and run" on a capability
	// the approval policy asks about, whose declared effects were shown to them
	// first. It answers that ask; a run the policy allows or refuses ignores it.
	Approved bool `json:"approved"`
}

var moduleInvokeRunner = func(
	ctx context.Context,
	runner *module.Runner,
	descriptor *modproto.Descriptor,
	request *modproto.Request,
) (*module.Result, error) {
	return runner.Invoke(ctx, descriptor, request)
}

// InvokeResult is one normalized invocation, shaped for rendering.
type InvokeResult struct {
	OK         bool               `json:"ok"`
	Module     string             `json:"module"`
	Capability string             `json:"capability"`
	DurationMS int64              `json:"duration_ms"`
	Error      *modproto.Error    `json:"error,omitempty"`
	Execution  modproto.Execution `json:"execution"`
	Warnings   []string           `json:"warnings"`
	// HostWarnings are the host's own findings about this invocation, kept
	// separate from the module's so a reader can tell who is complaining.
	HostWarnings []string        `json:"host_warnings"`
	Result       json.RawMessage `json:"result,omitempty"`
	Stderr       string          `json:"stderr,omitempty"`
	At           time.Time       `json:"at"`
}

func (h *Handler) handleModuleInvoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathModuleID(w, r)
	if !ok {
		return
	}

	var req invokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	home := config.GetHome()
	var target *moduletools.Installed
	for _, in := range moduletools.Discover(r.Context(), home) {
		if in.Descriptor != nil && in.Descriptor.Module == id {
			found := in
			target = &found
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "module not installed: " + id})
		return
	}
	if target.Disabled {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": fmt.Sprintf("module %q is disabled; re-enable it before invoking capabilities", id),
		})
		return
	}
	// Decided before anything is prepared or run: an artefact that already
	// exists cannot be un-produced by an error, and a provider already charged
	// cannot be un-charged.
	approved, status, err := h.invokeApproval(target.Descriptor, req.Capability, req.Approved)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	workspace, err := h.moduleWorkspace()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	out := InvokeResult{
		Module: id, Capability: req.Capability, At: time.Now(),
		Warnings: []string{}, HostWarnings: []string{},
	}

	// The operator's source roots, read only for a module that declares one. A
	// declared root that will not be granted is reported with the result, so a
	// module failing for want of its data says why.
	var sources map[string]string
	if len(moduletools.SourceRootNames(target.Descriptor)) > 0 {
		var err error
		sources, err = h.configuredSourceRoots()
		out.HostWarnings = append(out.HostWarnings, sourceRootHostWarnings(target.Descriptor, sources, err)...)
	}

	modReq := &modproto.Request{
		Capability: req.Capability,
		Roots:      moduletools.GrantRoots(target.Descriptor, home, workspace, sources),
		DeadlineMS: module.DefaultInvokeDeadlineMS,
	}
	if err := moduletools.PlaceInput(modReq, req.Input, approved); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	module.GrantBinaries(target.Descriptor, modReq)
	// Publishing is granted per approved call, as on the agent path: any other
	// run gets only what the module declared.
	module.ApplyGrants(target.Descriptor, modReq, module.GrantsFor(approved))

	res, err := moduleInvokeRunner(r.Context(), target.Runner, target.Descriptor, modReq)
	if res != nil {
		out.DurationMS = res.Duration.Milliseconds()
		out.Stderr = res.Stderr
		out.HostWarnings = append(out.HostWarnings, res.Warnings...)
		if res.Envelope != nil {
			out.OK = res.Envelope.OK
			out.Error = res.Envelope.Error
			if out.Error != nil {
				bounded := *out.Error
				bounded.Message = module.BoundText(bounded.Message, module.MaxErrorText)
				out.Error = &bounded
			}
			out.Execution = res.Envelope.Execution
			out.Warnings = res.Envelope.Warnings
			out.Result = res.Envelope.Result
		}
	}
	applyHostRefusal(&out, err)
	if out.Warnings == nil {
		out.Warnings = []string{}
	}
	if out.Execution.Artifacts == nil {
		out.Execution.Artifacts = []modproto.Artifact{}
	}

	writeJSON(w, http.StatusOK, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		_ = fmt.Errorf("encode response: %w", err)
	}
}

var _ = context.Background

// handleArtifact serves the bytes of an artefact a module produced.
//
// The path is resolved against the roots the HOST granted that module, never
// against a client-supplied path, so a browser cannot read outside a module's
// own directory by asking. That is the same confinement the invocation path
// enforces, applied to reads.
func (h *Handler) handleArtifact(w http.ResponseWriter, r *http.Request) {
	moduleID := r.URL.Query().Get("module")
	rootName := r.URL.Query().Get("root")
	rel := r.URL.Query().Get("path")
	if moduleID == "" || rootName == "" || rel == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "module, root and path are required"})
		return
	}
	if err := module.CheckID(moduleID); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	// Resolve through the real descriptor: a root the module never declared is
	// never granted, so it cannot be read even if a caller names it.
	var target *moduletools.Installed
	for _, in := range moduletools.Discover(r.Context(), config.GetHome()) {
		if in.Descriptor != nil && in.Descriptor.Module == moduleID {
			found := in
			target = &found
			break
		}
	}
	if target == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "module not installed"})
		return
	}

	home := config.GetHome()
	workspace, err := h.moduleWorkspace()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// The roots the module is granted, resolved without creating anything: a
	// GET used to create the module's state directories as a side effect.
	roots := moduletools.ArtifactRoots(target.Descriptor, home, workspace)
	abs, err := module.ResolveArtifact(&modproto.Request{Roots: roots},
		modproto.Artifact{Root: rootName, Path: rel})
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}

	f, err := os.Open(abs)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artefact not readable"})
		return
	}
	defer f.Close()

	// Content-Type is chosen by the HOST from the file extension, never from a
	// module-supplied media type. A module claiming text/html for arbitrary
	// bytes would otherwise get script execution in the cockpit's own origin.
	w.Header().Set("Content-Type", contentTypeFor(abs))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, filepath.Base(abs), time.Time{}, f)
}

// contentTypeFor maps a file extension to a media type the cockpit can render.
//
// Anything unrecognised is served as an octet stream rather than guessed at:
// a wrong guess that happens to be executable is the failure worth avoiding.
func contentTypeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4", ".m4v":
		return "video/mp4"
	case ".webm":
		return "video/webm"
	case ".mp3":
		return "audio/mpeg"
	case ".wav":
		return "audio/wav"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".json", ".jsonl":
		return "application/json"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".csv":
		return "text/csv; charset=utf-8"
	case ".txt", ".log":
		return "text/plain; charset=utf-8"

	// Types internal/view can draw but this endpoint used to refuse.
	//
	// The two lists have to agree: the host tells the cockpit an artefact is a
	// "document" or a "diagram", and the cockpit then renders a viewer for it.
	// Serving those same bytes as an octet stream means the card promises a
	// viewer the browser cannot use -- a diagram frame over bytes it will not
	// draw. Observed: a .mmd resolved to "diagram" and was served as
	// application/octet-stream.
	//
	// None of these is a scripting hazard: the office formats are inert
	// archives to a browser, and the diagram sources are plain text. text/plain
	// is deliberate for the diagram sources rather than a text/vnd.* type --
	// the cockpit reads the primitive, not this header, and text/plain is the
	// safest thing that still displays.
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".doc":
		return "application/msword"
	case ".rtf":
		return "application/rtf"
	case ".epub":
		return "application/epub+zip"
	case ".pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case ".ppt":
		return "application/vnd.ms-powerpoint"
	case ".mmd", ".dot", ".gv":
		return "text/plain; charset=utf-8"
	case ".tsv":
		return "text/tab-separated-values; charset=utf-8"

	default:
		// Anything unrecognised stays an octet stream rather than being guessed
		// at: a wrong guess that happens to be executable is the failure worth
		// avoiding, and it is why .svg above is the only markup type served.
		return "application/octet-stream"
	}
}

// applyHostRefusal records a host-side refusal on the result.
//
// A host-side refusal is a real answer rather than a 500: the module
// misbehaved and the user should see which rule it broke. It is also a
// FAILURE, whatever the module said about itself.
//
// That second part is the load-bearing one. A module reports ok in its own
// envelope; the host decides whether that envelope was admissible. Letting the
// module's ok survive a host refusal lets the module overrule the host -- an
// artifact with an absolute path rendered as a success card in the cockpit
// while the host had already judged its confinement uncheckable.
//
// The module's own error is preserved when it set one, because "the module
// failed AND broke the contract" is more useful than either half alone.
func applyHostRefusal(out *InvokeResult, err error) {
	if err == nil {
		return
	}
	out.HostWarnings = append(out.HostWarnings, err.Error())
	out.OK = false

	// Artifacts from a refused envelope are dropped, not merely flagged.
	//
	// The host judged this envelope inadmissible, which for an artifact means
	// its confinement could not be checked. Passing it on anyway would let the
	// cockpit render a card -- with a path, a size, a download link -- for a
	// file the host has already declined to vouch for. A card that says
	// "verified" about something nobody verified is worse than no card.
	//
	// The warning stays, so the reason is still visible.
	out.Execution.Artifacts = []modproto.Artifact{}

	if out.Error == nil {
		out.Error = &modproto.Error{
			Code:    modproto.ErrHostProtocolViolation,
			Message: err.Error(),
			Details: map[string]any{},
		}
	}
}

// invokeApproval decides a run from the Modules page by the approval policy
// (tools.approval), with origin web, as the agent's calls are decided. It
// returns whether the run is approved, or the status and error that refuse it.
//
// A run the policy allows goes ahead, approved when a rule allows it rather
// than the default. One it asks about goes ahead only when the person pressed
// "Approve and run", and is then approved. One it denies or hides does not run.
// Neither does a capability the module does not declare: the host cannot read
// the effects of something it has never been told about.
func (h *Handler) invokeApproval(d *modproto.Descriptor, capability string, requested bool) (bool, int, error) {
	c, ok := declaredCapability(d, capability)
	if !ok {
		return false, http.StatusForbidden,
			fmt.Errorf("module %q declares no capability %q, so it was not started", d.Module, capability)
	}
	policy, err := h.moduleApprovalPolicy()
	if err != nil {
		return false, http.StatusInternalServerError, err
	}
	decision := policy.Decide(moduletools.ApprovalTool(d, c), approval.OriginWeb)
	switch decision.Action {
	case approval.Allow:
		return decision.Approved(), 0, nil
	case approval.Ask:
		if !requested {
			return false, http.StatusForbidden, fmt.Errorf(
				"%s needs your approval before it runs. Press \"Approve and run\" on the"+
					" Modules page, against the effects it declares", c.ID)
		}
		return true, 0, nil
	default:
		return false, http.StatusForbidden, fmt.Errorf("%s is denied by the approval policy", c.ID)
	}
}

// declaredCapability finds the capability d declares under id.
func declaredCapability(d *modproto.Descriptor, id string) (modproto.Capability, bool) {
	for _, c := range d.Capabilities {
		if c.ID == id {
			return c, true
		}
	}
	return modproto.Capability{}, false
}
