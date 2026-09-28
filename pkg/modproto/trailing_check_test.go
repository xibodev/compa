package modproto

import "testing"

// TestTrailingVariants covers the stdout-purity rule across the shapes real
// modules actually emit.
//
// It exists because the first implementation checked for trailing content by
// attempting a second json.Decode, which only caught trailing VALID JSON. A
// trailing log line -- by far the likeliest violation, and the one the donor
// hit by merging stderr into stdout -- made that second decode fail, which the
// code then read as "nothing follows". The check was inverted for the common
// case and passed its own fixture suite until a negative fixture caught it.
//
// The clean cases matter just as much: rejecting an envelope for a trailing
// newline would fail every well-behaved module that ends its output with one.
func TestTrailingVariants(t *testing.T) {
	env := `{"protocol":"xibodev.module/v1","ok":true}`
	cases := map[string]string{
		"trailing log line":        env + "\nrendered in 1.2s",
		"trailing second doc":      env + "\n" + `{"a":1}`,
		"trailing bare word":       env + " done",
		"trailing partial json":    env + `{"unclosed":`,
		"trailing NDJSON progress": env + "\n" + `{"progress":0.5}` + "\n" + `{"progress":1.0}`,
	}
	for name, in := range cases {
		if _, err := DecodeEnvelope([]byte(in)); err == nil {
			t.Errorf("%s: accepted, want rejection", name)
		}
	}
	clean := map[string]string{
		"exact":            env,
		"trailing newline": env + "\n",
		"trailing spaces":  env + "  \n\t ",
	}
	for name, in := range clean {
		if _, err := DecodeEnvelope([]byte(in)); err != nil {
			t.Errorf("%s: rejected a clean envelope: %v", name, err)
		}
	}
}
