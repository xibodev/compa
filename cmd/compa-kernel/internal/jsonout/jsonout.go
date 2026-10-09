// Package jsonout lets a command print its result as one JSON document on
// stdout, for programs that run compa-kernel: the command's --json flag.
package jsonout

import (
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// Flag is the name of the flag that asks for JSON.
const Flag = "json"

// Usage describes the flag in help.
const Usage = "Print the result as one JSON document"

// Requested reports whether cmd runs with --json.
func Requested(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	flag := cmd.Flags().Lookup(Flag)
	return flag != nil && flag.Value.String() == "true"
}

// Accepted reports whether cmd has the --json flag.
func Accepted(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Flags().Lookup(Flag) != nil
}

// Progress is where cmd writes prompts and progress: stdout, or stderr when
// stdout carries the JSON document.
func Progress(cmd *cobra.Command) io.Writer {
	if Requested(cmd) {
		return cmd.ErrOrStderr()
	}
	return cmd.OutOrStdout()
}

// InArgs reports whether args, the command line without the program, ask
// for --json. main decides with it what it does before the flags are parsed:
// whether to print the banner, and where the logs go.
func InArgs(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--"+Flag {
			return true
		}
		if value, ok := strings.CutPrefix(arg, "--"+Flag+"="); ok {
			on, err := strconv.ParseBool(value)
			return err == nil && on
		}
	}
	return false
}

// Write prints v as one indented JSON document.
func Write(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// WriteError prints the document of a failed command: {"error": message}.
func WriteError(w io.Writer, err error) error {
	return Write(w, map[string]string{"error": err.Error()})
}
