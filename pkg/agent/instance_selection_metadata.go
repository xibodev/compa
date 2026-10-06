package agent

import (
	"strings"

	"github.com/xibodev/compa/v2/pkg/bus"
)

// instanceSelectionOutboundMetadata reports, for a turn that ran on the
// message's own model selection, what it selected and which target served
// it. A turn on the agent's model reports nothing.
func instanceSelectionOutboundMetadata(exec *turnExecution) map[string]string {
	if exec == nil || exec.requestedSelection == "" {
		return nil
	}
	return compactSelectionMetadata(exec.requestedSelection, exec.servedTarget, exec.servedIdentity)
}

func instanceSelectionResultMetadata(result turnResult) map[string]string {
	return compactSelectionMetadata(result.requestedSelection, result.servedTarget, result.servedIdentity)
}

func compactSelectionMetadata(requested, target, identity string) map[string]string {
	metadata := make(map[string]string, 3)
	if requested = strings.TrimSpace(requested); requested != "" {
		metadata[bus.MetadataKeyModelSelection] = requested
	}
	if target = strings.TrimSpace(target); target != "" {
		metadata[bus.MetadataKeyServedTarget] = target
		metadata["model_name"] = target
	}
	if identity = strings.TrimSpace(identity); identity != "" {
		metadata[bus.MetadataKeyServedIdentity] = identity
	}
	return metadata
}
