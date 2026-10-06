package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xibodev/compa/v2/pkg/approval"
)

func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultConfigSettings(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.Tools.Message.EffectiveTargets(); got != MessageTargetsCurrentChat {
		t.Errorf("message targets = %q", got)
	}
	if !cfg.Tools.InstallSkill.Enabled {
		t.Errorf("install_skill = %+v, want enabled", cfg.Tools.InstallSkill)
	}
	if !reflect.DeepEqual(cfg.Tools.Approval, approval.DefaultPolicy()) {
		t.Errorf("tools.approval = %+v, want the default policy", cfg.Tools.Approval)
	}
	if !cfg.Commands.OwnerOnly {
		t.Error("commands.owner_only should default to true")
	}
	if !cfg.Logging.RedactSecrets || cfg.Logging.EffectiveMaxSizeMB() != 10 || cfg.Logging.EffectiveMaxFiles() != 5 {
		t.Errorf("logging = %+v", cfg.Logging)
	}
	for name, ch := range cfg.Channels {
		if ch.Type == ChannelWeb {
			if ch.EffectiveDMPolicy() != DMPolicyOpen {
				t.Errorf("%s dm policy = %q, want open", name, ch.EffectiveDMPolicy())
			}
			continue
		}
		if ch.EffectiveDMPolicy() != DMPolicyPairing {
			t.Errorf("%s dm policy = %q, want pairing", name, ch.EffectiveDMPolicy())
		}
		if ch.EffectiveGroupPolicy() != GroupPolicyAllowlist {
			t.Errorf("%s group policy = %q, want allowlist", name, ch.EffectiveGroupPolicy())
		}
		if !ch.GroupTrigger.MentionOnly {
			t.Errorf("%s mention_only should default to true", name)
		}
	}
	maix, _ := cfg.Channels[ChannelMaixCam].GetDecoded()
	if s, ok := maix.(*MaixCamSettings); !ok || s.Host != "127.0.0.1" || s.Port == 18790 {
		t.Errorf("maixcam settings = %+v", maix)
	}
}

func TestEffectivePoliciesDeriveFromAllowFrom(t *testing.T) {
	cases := []struct {
		allow    []string
		dm, grp  string
		everyone bool
	}{
		{nil, DMPolicyPairing, GroupPolicyAllowlist, false},
		{[]string{"123"}, DMPolicyAllowlist, GroupPolicyAllowlist, false},
		{[]string{"*"}, DMPolicyOpen, GroupPolicyOpen, true},
		{[]string{" * ", "123"}, DMPolicyOpen, GroupPolicyOpen, true},
	}
	for _, tc := range cases {
		ch := &Channel{AllowFrom: tc.allow}
		if got := ch.EffectiveDMPolicy(); got != tc.dm {
			t.Errorf("allow %v: dm = %q, want %q", tc.allow, got, tc.dm)
		}
		if got := ch.EffectiveGroupPolicy(); got != tc.grp {
			t.Errorf("allow %v: group = %q, want %q", tc.allow, got, tc.grp)
		}
		if ch.AllowsEveryone() != tc.everyone {
			t.Errorf("allow %v: everyone = %v", tc.allow, ch.AllowsEveryone())
		}
	}
	explicit := &Channel{AllowFrom: []string{"*"}, DMPolicy: DMPolicyDisabled}
	if explicit.EffectiveDMPolicy() != DMPolicyDisabled {
		t.Error("an explicit dm_policy must win over allow_from")
	}
}

func TestLoadConfigKeepsExplicitValues(t *testing.T) {
	path := writeConfigFile(t, `{
  "channel_list": {
    "telegram": {"enabled": true, "type": "telegram", "dm_policy": "allowlist", "allow_from": ["42"], "group_trigger": {"mention_only": false}, "settings": {}}
  },
  "tools": {"message": {"enabled": true, "targets": "any"}},
  "commands": {"owner_only": false}
}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	tg := cfg.Channels["telegram"]
	if tg.DMPolicy != DMPolicyAllowlist || tg.GroupTrigger.MentionOnly {
		t.Errorf("telegram = %+v", tg)
	}
	// group_policy was not written; it derives from allow_from.
	if tg.EffectiveGroupPolicy() != GroupPolicyAllowlist {
		t.Errorf("group policy = %q", tg.EffectiveGroupPolicy())
	}
	if cfg.Tools.Message.Targets != MessageTargetsAny || cfg.Commands.OwnerOnly {
		t.Errorf("explicit settings changed: targets=%q owner_only=%v", cfg.Tools.Message.Targets, cfg.Commands.OwnerOnly)
	}
	// A setting the file lacks gets its default.
	if !reflect.DeepEqual(cfg.Tools.Approval, approval.DefaultPolicy()) {
		t.Errorf("tools.approval = %+v, want the default policy", cfg.Tools.Approval)
	}
}

// A channel entry answers in groups only when mentioned unless it says
// otherwise, as the default channels do.
func TestAChannelEntryWithoutMentionOnlyAnswersOnlyWhenMentioned(t *testing.T) {
	for name, tc := range map[string]struct {
		entry string
		want  bool
	}{
		"no group_trigger": {`{"type":"telegram","settings":{}}`, true},
		"prefixes only":    {`{"type":"telegram","group_trigger":{"prefixes":["!"]},"settings":{}}`, true},
		"explicit false":   {`{"type":"telegram","group_trigger":{"mention_only":false},"settings":{}}`, false},
	} {
		cfg, err := LoadConfig(writeConfigFile(t, `{"channel_list":{"mybot":`+tc.entry+`}}`))
		if err != nil {
			t.Fatalf("%s: LoadConfig: %v", name, err)
		}
		bot := cfg.Channels["mybot"]
		if bot == nil || bot.Name() != "mybot" {
			t.Fatalf("%s: channel = %+v", name, bot)
		}
		if got := bot.GroupTrigger.MentionOnly; got != tc.want {
			t.Errorf("%s: mention_only = %v, want %v", name, got, tc.want)
		}
	}
}

func TestLoadConfigRejectsUnknownPolicyValues(t *testing.T) {
	cases := map[string]string{
		"dm_policy":      `{"channel_list":{"telegram":{"type":"telegram","dm_policy":"everyone","settings":{}}}}`,
		"group_policy":   `{"channel_list":{"telegram":{"type":"telegram","group_policy":"pairing","settings":{}}}}`,
		"chats":          `{"channel_list":{"whatsapp_native":{"type":"whatsapp_native","settings":{"chats":"friends"}}}}`,
		"targets":        `{"tools":{"message":{"targets":"everyone"}}}`,
		"tools.approval": `{"tools":{"approval":{"rules":[{"tool":"exec","action":"maybe"}]}}}`,
	}
	for name, content := range cases {
		if _, err := LoadConfig(writeConfigFile(t, content)); err == nil {
			t.Errorf("%s: an unknown value should fail loading", name)
		}
	}
}

// The default policy asks before costly module capabilities and skill
// installs; a config's own rules replace it.
func TestApprovalPolicyDefaultsAndRules(t *testing.T) {
	cfg, err := LoadConfig(writeConfigFile(t, `{}`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !reflect.DeepEqual(cfg.Tools.Approval, approval.DefaultPolicy()) {
		t.Fatalf("tools.approval = %+v, want the default policy", cfg.Tools.Approval)
	}

	cfg, err = LoadConfig(writeConfigFile(t,
		`{"tools":{"approval":{"default":"ask","rules":[{"tool":"exec","origin":["chat"],"action":"deny"}]}}}`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := approval.Policy{Default: approval.Ask, Rules: []approval.Rule{
		{Tool: "exec", Origin: []approval.Origin{approval.OriginChat}, Action: approval.Deny},
	}}
	if !reflect.DeepEqual(cfg.Tools.Approval, want) {
		t.Fatalf("tools.approval = %+v, want %+v", cfg.Tools.Approval, want)
	}
}

func TestSaveConfigWritesExplicitSettings(t *testing.T) {
	path := writeConfigFile(t, `{"channel_list":{"telegram":{"enabled":true,"type":"telegram","settings":{}}}}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Channels map[string]struct {
			GroupTrigger map[string]any `json:"group_trigger"`
		} `json:"channel_list"`
		Commands map[string]any `json:"commands"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if _, ok := saved.Channels["telegram"].GroupTrigger["mention_only"]; !ok {
		t.Errorf("mention_only must be written explicitly, got %v", saved.Channels["telegram"].GroupTrigger)
	}
	if v, ok := saved.Commands["owner_only"]; !ok || v != true {
		t.Errorf("saved commands = %v", saved.Commands)
	}

	// Loading the saved file again changes nothing.
	again, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Channels["telegram"].EffectiveDMPolicy() != DMPolicyPairing || !again.Commands.OwnerOnly {
		t.Error("a saved config must load with the same behavior")
	}
}
