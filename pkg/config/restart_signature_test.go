package config

import (
	"maps"
	"testing"
)

// Signatures have the same digest exactly when they are Equal, secrets
// included; a nil signature has none.
func TestRestartSignatureDigest(t *testing.T) {
	signature := RestartSignature{"models": "default=x", "channel:telegram": `{"token":"secret-1"}`}
	if signature.Digest() == "" || signature.Digest() != maps.Clone(signature).Digest() {
		t.Fatalf("equal signatures: digests %q and %q", signature.Digest(), maps.Clone(signature).Digest())
	}
	for name, other := range map[string]RestartSignature{
		"another secret":         {"models": "default=x", "channel:telegram": `{"token":"secret-2"}`},
		"a key less":             {"models": "default=x"},
		"text moved across keys": {"models": "default=x", "channel:telegra": `m{"token":"secret-1"}`},
	} {
		if other.Digest() == signature.Digest() {
			t.Errorf("%s: same digest", name)
		}
	}
	if got := RestartSignature(nil).Digest(); got != "" {
		t.Errorf("nil signature digest = %q, want none", got)
	}

	cfg := DefaultConfig()
	cfg.Tools.WriteFile.Enabled = true
	before := NewRestartSignature(cfg).Digest()
	cfg.Tools.WriteFile.Enabled = false
	if NewRestartSignature(cfg).Digest() == before {
		t.Error("a tool setting change kept the digest")
	}
}
