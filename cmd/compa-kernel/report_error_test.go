package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/xibodev/compa/v3/cmd/compa-kernel/internal/jsonout"
	"github.com/xibodev/compa/v3/pkg/config"
)

// A command run with --json reports a failure as {"error": ...} on stdout;
// anything else gets the panel on stderr.
func TestReportError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(config.EnvConfig, path)
	require.NoError(t, config.SaveConfig(path, config.DefaultConfig()))

	cases := []struct {
		name string
		args []string
		// wantJSON is the document on stdout; "" wants the panel.
		wantJSON string
	}{
		{"--json", []string{"model", "ping", "ghost", "--json"}, `{"error": "provider instance \"ghost\" not found"}`},
		{"flags fail before --json is parsed", []string{"model", "ping", "--bogus", "--json"}, `{"error": "unknown flag: --bogus"}`},
		{"auth status --json", []string{"auth", "status", "extra", "--json"}, `{"error": "unknown command \"extra\" for \"compa-kernel auth status\""}`},
		{"no --json", []string{"model", "ping", "ghost"}, ""},
		{"a command without --json", []string{"version", "--json"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := NewRootCommand()
			root.SetArgs(tc.args)
			last, err := root.ExecuteC()
			require.Error(t, err)

			var stdout, stderr bytes.Buffer
			reportError(root, last, err, jsonout.InArgs(tc.args), &stdout, &stderr)
			if tc.wantJSON != "" {
				assert.JSONEq(t, tc.wantJSON, stdout.String())
				assert.Empty(t, stderr.String())
				return
			}
			assert.Empty(t, stdout.String())
			assert.NotEmpty(t, stderr.String())
		})
	}
}
