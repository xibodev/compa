package agent

import (
	runtimeevents "github.com/xibodev/compa/v2/pkg/events"
	"github.com/xibodev/compa/v2/pkg/tools"
)

// Tool is the kernel's public tool contract, re-exported so an embedder does
// not have to reach into pkg/tools/shared to satisfy ToolProvider.
//
// A four-method interface (Name, Description, Parameters, Execute) and nothing
// more. Keeping the embedding surface this small is what lets a standalone
// product bind its own capabilities natively.
type Tool = tools.Tool

// AgentLoopOption configures an AgentLoop at construction time.
type AgentLoopOption func(*AgentLoop)

// WithModelResolver sets how the loop resolves model selections: each
// agent's model, the image and light models, /switch model, hook model
// rewrites and per-message selections, always against the loop's current
// config. nil keeps the default, modelservice.NewResolver().Resolve, which
// reads the saved catalogs and the auth store.
func WithModelResolver(resolve ModelResolver) AgentLoopOption {
	return func(al *AgentLoop) {
		if resolve != nil {
			al.resolveModel = resolve
		}
	}
}

// WithRuntimeEvents injects the runtime event bus used for new observation APIs.
//
// The injected bus is treated as externally owned and will not be closed by
// AgentLoop.Close. Passing nil leaves the default owned runtime bus enabled.
func WithRuntimeEvents(bus runtimeevents.Bus) AgentLoopOption {
	return func(al *AgentLoop) {
		if bus == nil {
			return
		}
		al.runtimeEvents = bus
		al.ownsRuntimeEvents = false
	}
}

// ToolProvider contributes tools to an agent from outside the kernel.
//
// THIS IS THE KERNEL'S ONLY DOOR FOR NON-GENERIC CAPABILITIES, and it exists so
// the kernel can stay Layer 1.
//
// The kernel previously imported internal/moduletools directly and called it
// from registerSharedTools. That was a Layer 1 -> Layer 2 inversion,
// disqualifying twice over: architecturally a standalone product embedding the
// kernel dragged in module discovery, the v2 contract gate and bounded
// subprocess execution it would never use; and mechanically internal/ packages
// CANNOT BE IMPORTED by an external module, so any sibling embedding the kernel
// would simply fail to compile.
//
// That one import pulled in six Layer-2/3 packages transitively -- the module
// host, subprocess execution, both wire protocols, the contract gate, and the
// cockpit's artefact view vocabulary. Removing it drops all six at once; the
// audit found the other 55 of 56 transitive packages already generic.
//
// WHAT CROSSES THE BOUNDARY IS DELIBERATELY PLAIN. Tools arrive as
// tools.Tool, a four-method interface; the descriptive lines are []string; the
// knowledge loader is a plain func. No provider type reaches the agent struct,
// which is what makes the kernel independently consumable rather than merely
// separately compiled.
//
// Three shapes this serves:
//
//	full Compa  -- supplies a provider backed by the detached-module host
//	standalone   -- supplies a NATIVE, in-process provider binding its own
//	                capabilities, paying no subprocess or protocol cost to
//	                host itself
//	bare kernel  -- supplies none, and is a plain conversational runtime
type ToolProvider interface {
	// RegisterTools contributes tools via register and returns one-line
	// summaries for the agent's context.
	//
	// workspace is the AGENT'S OWN working directory, and it is passed rather
	// than captured because it is resolved PER AGENT inside the registration
	// loop -- the composition root does not know it. An earlier version of this
	// interface omitted it, which only surfaced when wiring the call sites:
	// the provider would have had to invent a workspace, and every agent would
	// have shared one.
	//
	// The returned knowledge function is optional -- nil is valid, and means
	// this provider has no per-selection knowledge to load. It maps a
	// provider-defined identifier to overlays, skills and warnings.
	RegisterTools(workspace string, register func(Tool)) (summaries []string,
		knowledge func(id string) (overlays string, skills string, warnings []string))
}

// WithToolProviders injects capability providers at construction time.
//
// Called once per agent during shared-tool registration, in the order given.
// Summaries accumulate across providers; the LAST non-nil knowledge loader
// wins, because a single selection resolves to a single provider today and
// silently merging two loaders would make an ambiguous selection look decided.
//
// Passing none is fully supported and is the standalone-kernel case.
func WithToolProviders(providers ...ToolProvider) AgentLoopOption {
	return func(al *AgentLoop) {
		for _, p := range providers {
			if p != nil {
				al.toolProviders = append(al.toolProviders, p)
			}
		}
	}
}
