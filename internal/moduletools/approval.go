package moduletools

import (
	"github.com/xibodev/compa/v3/pkg/approval"
	"github.com/xibodev/compa/v3/pkg/modproto"
)

// ApprovalInfo tells the approval policy (tools.approval) what this tool is: a
// capability of its module, with the hints of the effects it declares. The
// policy decides each call before Execute runs.
func (t *CapabilityTool) ApprovalInfo() approval.Info {
	e := t.capability.Effects
	return approval.Info{
		Source:  approval.ModuleSource(t.descriptor.Module),
		Name:    t.capability.ID,
		Hints:   approval.ModuleHints(e.Network, e.ExternalWrites, e.CostKnown),
		Effects: e,
	}
}

// ApprovalTool is the policy's view of capability c of module d, as the agent
// sees it. The Modules page and module-invoke decide on it, so a capability is
// decided alike wherever it is called from.
func ApprovalTool(d *modproto.Descriptor, c modproto.Capability) approval.Tool {
	tool, _ := approval.Describe(ToolName(d.Module, c.ID), &CapabilityTool{descriptor: d, capability: c})
	return tool
}
