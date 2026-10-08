package jsonout

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/spf13/cobra"
)

func TestInArgs(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"model"}, false},
		{[]string{"model", "--json"}, true},
		{[]string{"--json", "model"}, true},
		{[]string{"model", "--json=true"}, true},
		{[]string{"model", "--json=1"}, true},
		{[]string{"model", "--json=false"}, false},
		{[]string{"model", "--json=maybe"}, false},
		{[]string{"model", "--jsonl"}, false},
		{[]string{"model", "-json"}, false},
		{[]string{"agent", "--", "--json"}, false},
	}
	for _, tc := range cases {
		if got := InArgs(tc.args); got != tc.want {
			t.Errorf("InArgs(%q) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func newCommand(withFlag bool) *cobra.Command {
	cmd := &cobra.Command{Use: "test", RunE: func(*cobra.Command, []string) error { return nil }}
	if withFlag {
		cmd.Flags().Bool(Flag, false, Usage)
	}
	return cmd
}

func TestRequestedAndAccepted(t *testing.T) {
	if Requested(nil) || Accepted(nil) {
		t.Fatal("a nil command neither has nor asks for --json")
	}

	without := newCommand(false)
	if Requested(without) || Accepted(without) {
		t.Fatal("a command without the flag neither has nor asks for --json")
	}

	with := newCommand(true)
	if !Accepted(with) || Requested(with) {
		t.Fatal("before --json is given, the command has the flag but does not ask for JSON")
	}
	if err := with.Flags().Set(Flag, "true"); err != nil {
		t.Fatal(err)
	}
	if !Requested(with) {
		t.Fatal("--json was given; Requested = false")
	}
}

func TestProgressMovesToStderrWithJSON(t *testing.T) {
	cmd := newCommand(true)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if Progress(cmd) != io.Writer(&stdout) {
		t.Fatal("without --json, progress goes to stdout")
	}
	if err := cmd.Flags().Set(Flag, "true"); err != nil {
		t.Fatal(err)
	}
	if Progress(cmd) != io.Writer(&stderr) {
		t.Fatal("with --json, progress goes to stderr")
	}
}

// decodeOne decodes the only JSON document in data.
func decodeOne(t *testing.T, data []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %q: %v", data, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("%q holds more than one JSON document", data)
	}
}

func TestWriteErrorPrintsOneDocument(t *testing.T) {
	var out bytes.Buffer
	if err := WriteError(&out, errors.New(`choose with: compa-kernel model <instance-id>/<model-id>`)); err != nil {
		t.Fatal(err)
	}
	var doc map[string]string
	decodeOne(t, out.Bytes(), &doc)
	if len(doc) != 1 || doc["error"] != "choose with: compa-kernel model <instance-id>/<model-id>" {
		t.Fatalf("document = %v", doc)
	}
	if !bytes.Contains(out.Bytes(), []byte("<instance-id>")) {
		t.Fatalf("output %q escapes < and >", out.String())
	}
}
