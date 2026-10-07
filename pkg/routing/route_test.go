package routing

import (
	"testing"

	"github.com/xibodev/compa/v3/pkg/bus"
	"github.com/xibodev/compa/v3/pkg/config"
)

func testConfig(agents []config.AgentConfig) *config.Config {
	return &config.Config{
		Agents: config.AgentsConfig{
			Defaults: config.AgentDefaults{
				Workspace: "/tmp/compa-test",
				ModelName: "gpt-4",
			},
			List: agents,
		},
		Session: config.SessionConfig{
			Dimensions: []string{"sender"},
		},
	}
}

func TestResolveRoute_DefaultAgent_NoBindings(t *testing.T) {
	cfg := testConfig(nil)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{
		Channel:  "telegram",
		ChatType: "direct",
		SenderID: "user1",
	})

	if route.AgentID != DefaultAgentID {
		t.Errorf("AgentID = %q, want %q", route.AgentID, DefaultAgentID)
	}
	if route.MatchedBy != "default" {
		t.Errorf("MatchedBy = %q, want 'default'", route.MatchedBy)
	}
	if len(route.SessionPolicy.Dimensions) != 1 || route.SessionPolicy.Dimensions[0] != "sender" {
		t.Errorf("SessionPolicy.Dimensions = %v, want [sender]", route.SessionPolicy.Dimensions)
	}
	if route.SessionPolicy.IdentityLinks != nil {
		t.Errorf("SessionPolicy.IdentityLinks = %v, want nil", route.SessionPolicy.IdentityLinks)
	}
}

func TestResolveRoute_UsesNormalizedInboundContextFields(t *testing.T) {
	cfg := testConfig([]config.AgentConfig{{ID: "sales", Default: true}})
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{
		Channel:  "Telegram",
		Account:  "Bot2",
		ChatType: "direct",
		SenderID: "user123",
	})

	if route.AgentID != "sales" {
		t.Errorf("AgentID = %q, want 'sales'", route.AgentID)
	}
	if route.Channel != "telegram" {
		t.Errorf("Channel = %q, want 'telegram'", route.Channel)
	}
	if route.AccountID != "bot2" {
		t.Errorf("AccountID = %q, want 'bot2'", route.AccountID)
	}
	if route.MatchedBy != "default" {
		t.Errorf("MatchedBy = %q, want 'default'", route.MatchedBy)
	}
}

func TestResolveRoute_DispatchFirstMatchWins(t *testing.T) {
	cfg := testConfig([]config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
		{ID: "sales"},
	})
	cfg.Agents.Dispatch = &config.DispatchConfig{
		Rules: []config.DispatchRule{
			{
				Name:  "support-group",
				Agent: "support",
				When: config.DispatchSelector{
					Channel: "telegram",
					Chat:    "group:-100123",
				},
			},
			{
				Name:  "vip-in-group",
				Agent: "sales",
				When: config.DispatchSelector{
					Channel: "telegram",
					Chat:    "group:-100123",
					Sender:  "12345",
				},
			},
		},
	}
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{
		Channel:  "telegram",
		ChatID:   "-100123",
		ChatType: "group",
		SenderID: "12345",
	})

	if route.AgentID != "support" {
		t.Fatalf("AgentID = %q, want support", route.AgentID)
	}
	if route.MatchedBy != "dispatch.rule:support-group" {
		t.Fatalf("MatchedBy = %q, want dispatch.rule:support-group", route.MatchedBy)
	}
}

func TestResolveRoute_DispatchOverridesSessionDimensions(t *testing.T) {
	cfg := testConfig([]config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	})
	cfg.Session.Dimensions = []string{"chat"}
	cfg.Agents.Dispatch = &config.DispatchConfig{
		Rules: []config.DispatchRule{
			{
				Name:  "support-dm",
				Agent: "support",
				When: config.DispatchSelector{
					Channel: "telegram",
					Chat:    "direct:user-1",
				},
				SessionDimensions: []string{"chat", "sender"},
			},
		},
	}
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{
		Channel:  "telegram",
		ChatID:   "user-1",
		ChatType: "direct",
		SenderID: "user-1",
	})

	if route.AgentID != "support" {
		t.Fatalf("AgentID = %q, want support", route.AgentID)
	}
	if got := route.SessionPolicy.Dimensions; len(got) != 2 || got[0] != "chat" || got[1] != "sender" {
		t.Fatalf("SessionPolicy.Dimensions = %v, want [chat sender]", got)
	}
}

func TestResolveRoute_DispatchMentionedRule(t *testing.T) {
	cfg := testConfig([]config.AgentConfig{
		{ID: "main", Default: true},
		{ID: "support"},
	})
	mentioned := true
	cfg.Agents.Dispatch = &config.DispatchConfig{
		Rules: []config.DispatchRule{
			{
				Name:  "slack-mentions",
				Agent: "support",
				When: config.DispatchSelector{
					Channel:   "slack",
					Space:     "workspace:t001",
					Mentioned: &mentioned,
				},
			},
		},
	}
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{
		Channel:   "slack",
		ChatID:    "C123",
		ChatType:  "channel",
		SpaceID:   "T001",
		SpaceType: "workspace",
		SenderID:  "U123",
		Mentioned: true,
	})

	if route.AgentID != "support" {
		t.Fatalf("AgentID = %q, want support", route.AgentID)
	}
}

func TestResolveRoute_InvalidAgentFallsToDefault(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "main", Default: true},
	}
	cfg := testConfig(agents)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{Channel: "telegram"})

	if route.AgentID != "main" {
		t.Errorf("AgentID = %q, want 'main' (invalid agent should fall to default)", route.AgentID)
	}
}

func TestResolveRoute_DefaultAgentSelection(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "alpha"},
		{ID: "beta", Default: true},
		{ID: "gamma"},
	}
	cfg := testConfig(agents)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{Channel: "cli"})

	if route.AgentID != "beta" {
		t.Errorf("AgentID = %q, want 'beta' (marked as default)", route.AgentID)
	}
}

func TestResolveRoute_NoDefaultUsesFirst(t *testing.T) {
	agents := []config.AgentConfig{
		{ID: "alpha"},
		{ID: "beta"},
	}
	cfg := testConfig(agents)
	r := NewRouteResolver(cfg)

	route := r.ResolveRoute(bus.InboundContext{Channel: "cli"})

	if route.AgentID != "alpha" {
		t.Errorf("AgentID = %q, want 'alpha' (first in list)", route.AgentID)
	}
}

func TestResolveDefaultAgentID(t *testing.T) {
	tests := []struct {
		name   string
		agents []config.AgentConfig
		want   string
	}{
		{name: "implicit main", want: DefaultAgentID},
		{name: "default flag wins over a main entry", agents: []config.AgentConfig{
			{ID: "main"}, {ID: "Sales", Default: true},
		}, want: "sales"},
		{name: "first entry without a default", agents: []config.AgentConfig{
			{ID: "alpha"}, {ID: "main"},
		}, want: "alpha"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveDefaultAgentID(testConfig(tt.agents)); got != tt.want {
				t.Fatalf("ResolveDefaultAgentID() = %q, want %q", got, tt.want)
			}
		})
	}
	if got := ResolveDefaultAgentID(nil); got != DefaultAgentID {
		t.Fatalf("ResolveDefaultAgentID(nil) = %q, want %q", got, DefaultAgentID)
	}
}
