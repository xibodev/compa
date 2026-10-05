package hardwaretools

import (
	"context"
	"runtime"
	"strings"
	"testing"
)

func TestHardwareReadsThatWriteRequireConfirm(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(context.Context, map[string]any) *ToolResult
		args map[string]any
	}{
		{"i2c scan", NewI2CTool().Execute, map[string]any{"action": "scan", "bus": "1"}},
		{"i2c register read", NewI2CTool().Execute, map[string]any{
			"action": "read", "bus": "1", "address": float64(0x40), "register": float64(1),
		}},
		{"spi read", NewSPITool().Execute, map[string]any{"action": "read", "device": "0.0", "length": float64(2)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.run(ctx, c.args)
			if !r.IsError || !strings.Contains(r.ForLLM, "requires confirm: true") {
				t.Fatalf("expected a confirmation error, got %q", r.ForLLM)
			}
		})
	}

	// A plain read sends nothing, so it needs no confirmation.
	r := NewI2CTool().Execute(ctx, map[string]any{"action": "read", "bus": "1", "address": float64(0x40)})
	if strings.Contains(r.ForLLM, "requires confirm") {
		t.Fatalf("plain read should not need confirmation: %q", r.ForLLM)
	}
}

func TestHardwareToolsAvailableOnlyWhereSupported(t *testing.T) {
	linux := runtime.GOOS == "linux"
	if NewI2CTool().Available() != linux || NewSPITool().Available() != linux {
		t.Fatalf("I2C/SPI availability must follow Linux support on %s", runtime.GOOS)
	}
	serial := linux || runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	if NewSerialTool().Available() != serial {
		t.Fatalf("serial availability is wrong on %s", runtime.GOOS)
	}
}
