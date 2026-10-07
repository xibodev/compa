package modproto

import (
	"encoding/json"
	"testing"
)

// TestExtraCannotShadowHostFields is the security property behind Extra: a
// module reading root-level arguments must not be able to widen its own
// authority by naming a host-owned field.
func TestExtraCannotShadowHostFields(t *testing.T) {
	r := Request{
		Protocol:   ProtocolID,
		Capability: "fake.echo",
		RequestID:  "req_real",
		Roots:      map[string]Root{"workspace": {Path: "/real", Mode: "ro"}},
		DeadlineMS: 1000,
		Extra: map[string]json.RawMessage{
			"tool":        json.RawMessage(`"media_probe"`),
			"request_id":  json.RawMessage(`"req_forged"`),
			"roots":       json.RawMessage(`{"everything":{"path":"/","mode":"rw"}}`),
			"deadline_ms": json.RawMessage(`999999999`),
			"binaries":    json.RawMessage(`{"sh":"/bin/sh"}`),
		},
	}
	r.Normalize()

	blob, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got map[string]json.RawMessage
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// The legitimate passthrough survives.
	if string(got["tool"]) != `"media_probe"` {
		t.Errorf("tool = %s, want the passed-through value", got["tool"])
	}

	// Every host-owned field keeps the host's value.
	if string(got["request_id"]) != `"req_real"` {
		t.Errorf("request_id = %s; a module must not be able to forge correlation", got["request_id"])
	}
	if string(got["deadline_ms"]) != `1000` {
		t.Errorf("deadline_ms = %s; a module must not be able to widen its own bounds", got["deadline_ms"])
	}
	var roots map[string]Root
	if err := json.Unmarshal(got["roots"], &roots); err != nil {
		t.Fatalf("roots: %v", err)
	}
	if _, widened := roots["everything"]; widened {
		t.Error("a module widened its own filesystem roots through Extra")
	}
	if roots["workspace"].Mode != "ro" {
		t.Errorf("workspace mode = %q; a module must not be able to upgrade ro to rw", roots["workspace"].Mode)
	}
	var bins map[string]string
	if err := json.Unmarshal(got["binaries"], &bins); err != nil {
		t.Fatalf("binaries: %v", err)
	}
	if len(bins) != 0 {
		t.Errorf("binaries = %v; a module must not be able to grant itself an executable", bins)
	}
}
