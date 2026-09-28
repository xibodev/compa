package session

import "testing"

type testScopeReader struct {
	scope *SessionScope
}

func (r testScopeReader) GetSessionScope(sessionKey string) *SessionScope {
	return CloneScope(r.scope)
}

func TestIsOpaqueSessionKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"sk_v1_abc", true},
		{" SK_V1_ABC ", true},
		{"agent:main:direct:user123", false},
		{"custom-key", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := IsOpaqueSessionKey(tt.key); got != tt.want {
			t.Fatalf("IsOpaqueSessionKey(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}

func TestBuildMainSessionKey(t *testing.T) {
	got := BuildMainSessionKey("Main")
	if !IsOpaqueSessionKey(got) {
		t.Fatalf("BuildMainSessionKey() = %q, want opaque key", got)
	}
	if got != BuildMainSessionKey("main") {
		t.Fatalf("BuildMainSessionKey() = %q, want a key independent of agent ID case", got)
	}
	if got == BuildMainSessionKey("support") {
		t.Fatalf("BuildMainSessionKey() = %q, want distinct keys per agent", got)
	}
}

func TestResolveAgentID_UsesSessionScope(t *testing.T) {
	store := testScopeReader{
		scope: &SessionScope{
			Version: ScopeVersionV1,
			AgentID: "Support",
			Channel: "slack",
		},
	}

	if got := ResolveAgentID(store, "sk_v1_anything"); got != "support" {
		t.Fatalf("ResolveAgentID() = %q, want support", got)
	}
}

func TestResolveAgentID_WithoutScopeIsEmpty(t *testing.T) {
	if got := ResolveAgentID(nil, "sk_v1_anything"); got != "" {
		t.Fatalf("ResolveAgentID(nil) = %q, want empty", got)
	}
	if got := ResolveAgentID(testScopeReader{}, "sk_v1_anything"); got != "" {
		t.Fatalf("ResolveAgentID(no scope) = %q, want empty", got)
	}
}
