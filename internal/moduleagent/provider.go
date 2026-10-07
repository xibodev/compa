package moduleagent

import (
	"context"

	"github.com/xibodev/compa/v3/internal/moduletools"
	"github.com/xibodev/compa/v3/pkg/agent"
	toolshared "github.com/xibodev/compa/v3/pkg/tools/shared"
)

// Package moduleagent binds the detached-module host to the agent kernel.
//
// IT IS ITS OWN PACKAGE FOR A PACKAGING REASON, not a stylistic one. This
// adapter must import BOTH the module host and the kernel. Living inside
// moduletools, it dragged pkg/agent into everything that manages modules --
// including the web shell, which only lists and installs them and never runs an
// agent. The shell linked an entire agent runtime it does not call.
//
// Splitting it means the shell imports moduletools (management) and the
// composition roots import moduleagent (wiring), so each binary carries only
// what it uses.

// Provider adapts the detached-module host to the kernel's ToolProvider door.
//
// THIS TYPE IS WHY THE KERNEL CAN BE EMBEDDED. The kernel used to import this
// package directly, which made it depend on module discovery, subprocess
// execution, both wire protocols, the contract gate and the cockpit's artefact
// view -- and made it impossible for an external module to import at all, since
// Go forbids importing internal/.
//
// The direction is now inverted: the module host knows about the kernel, which
// is correct, because Layer 2 is optional infrastructure built ON the kernel
// rather than part of it.
//
// Full Compa constructs one of these at its composition root. A standalone
// product constructs its own native provider instead and never links this
// package.
type Provider struct {
	// Home is the host state root; modules are discovered beneath it.
	//
	// The WORKSPACE is not stored here: it is resolved per agent and arrives as
	// an argument, so one provider serves every agent rather than each needing
	// its own.
	Home string
}

// NewProvider returns a provider backed by the modules installed under home.
func NewProvider(home string) *Provider {
	return &Provider{Home: home}
}

// RegisterTools satisfies agent.ToolProvider.
//
// Behaviour is identical to the call the kernel used to make directly: every
// capability of every enabled module becomes a tool, summaries describe them in
// one line each, and the knowledge loader closes over the modules discovered
// HERE so selecting one costs a file read rather than re-describing every
// installed module as a subprocess.
func (p *Provider) RegisterTools(workspace string, register func(agent.Tool)) (
	[]string, func(string) (string, string, []string)) {

	if p == nil {
		return nil, nil
	}
	summaries, installed := moduletools.RegisterTools(
		context.Background(),
		p.Home,
		workspace,
		func(t toolshared.Tool) { register(t) },
	)
	return summaries, moduletools.KnowledgeLoader(installed)
}
