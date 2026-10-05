package hardwaretools

import (
	"fmt"
	"runtime"

	toolshared "github.com/xibodev/compa/pkg/tools/shared"
)

type ToolResult = toolshared.ToolResult

func ErrorResult(message string) *ToolResult {
	return toolshared.ErrorResult(message)
}

func SilentResult(forLLM string) *ToolResult {
	return toolshared.SilentResult(forLLM)
}

// requireConfirm refuses an operation that sends bytes to a device unless
// the call carries confirm: true.
func requireConfirm(args map[string]any, why string) *ToolResult {
	if confirm, _ := args["confirm"].(bool); confirm {
		return nil
	}
	return ErrorResult(fmt.Sprintf(
		"this operation requires confirm: true because %s. Please confirm with the user first.", why))
}

// Available keeps the tools out of the registry where they can't work.
func (t *I2CTool) Available() bool { return runtime.GOOS == "linux" }

func (t *SPITool) Available() bool { return runtime.GOOS == "linux" }

func (t *SerialTool) Available() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "windows":
		return true
	}
	return false
}
