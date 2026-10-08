package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xibodev/compa/v3/pkg/approval"
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
	if got := cfg.Tools.Message.EffectiveTargets(); got != MessageTargetsAny {
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
		if ch.DMPolicy != "" || ch.GroupPolicy != "" || ch.GroupTrigger.MentionOnly || len(ch.GroupTrigger.Prefixes) > 0 {
			t.Errorf("%s sets a retired access setting: %+v", name, ch)
		}
	}
	maix, _ := cfg.Channels[ChannelMaixCam].GetDecoded()
	if s, ok := maix.(*MaixCamSettings); !ok || s.Host != "127.0.0.1" || s.Port == 18790 {
		t.Errorf("maixcam settings = %+v", maix)
	}
}

func TestLoadConfigKeepsExplicitValues(t *testing.T) {
	path := writeConfigFile(t, `{
  "channel_list": {
    "telegram": {"enabled": true, "type": "telegram", "allow_from": ["42"], "settings": {}}
  },
  "tools": {"message": {"enabled": true, "targets": "any"}},
  "commands": {"owner_only": false}
}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if tg := cfg.Channels["telegram"]; len(tg.AllowFrom) != 1 || tg.AllowFrom[0] != "42" {
		t.Errorf("telegram = %+v", tg)
	}
	if cfg.Tools.Message.Targets != MessageTargetsAny || cfg.Commands.OwnerOnly {
		t.Errorf("explicit settings changed: targets=%q owner_only=%v", cfg.Tools.Message.Targets, cfg.Commands.OwnerOnly)
	}
	if (MessageToolsConfig{}).EffectiveTargets() != MessageTargetsAny ||
		(MessageToolsConfig{Targets: MessageTargetsCurrentChat}).EffectiveTargets() != MessageTargetsCurrentChat {
		t.Error("message targets: any by default, current_chat when set")
	}
	// A setting the file lacks gets its default.
	if !reflect.DeepEqual(cfg.Tools.Approval, approval.DefaultPolicy()) {
		t.Errorf("tools.approval = %+v, want the default policy", cfg.Tools.Approval)
	}
}

// dm_policy, group_policy and group_trigger are no longer settings: a
// config.json an earlier version wrote, whatever their values, still loads,
// without them, and saving does not write them back.
func TestRetiredAccessSettingsLoadAndAreDropped(t *testing.T) {
	path := writeConfigFile(t, `{"channel_list":{"mybot":{"type":"telegram","allow_from":["42"],`+
		`"dm_policy":"open","group_policy":"whatever","group_trigger":{"mention_only":true,"prefixes":["!"]},"settings":{}}}}`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	bot := cfg.Channels["mybot"]
	if bot == nil || bot.Name() != "mybot" || len(bot.AllowFrom) != 1 {
		t.Fatalf("channel = %+v", bot)
	}
	if bot.DMPolicy != "" || bot.GroupPolicy != "" || bot.GroupTrigger.MentionOnly || len(bot.GroupTrigger.Prefixes) > 0 {
		t.Fatalf("retired settings kept: %+v", bot)
	}

	if err := SaveConfig(path, cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Channels map[string]map[string]json.RawMessage `json:"channel_list"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	for name, entry := range saved.Channels {
		for _, key := range []string{"dm_policy", "group_policy", "group_trigger"} {
			if _, ok := entry[key]; ok {
				t.Errorf("channel %s saved %s: %s", name, key, entry[key])
			}
		}
	}
}

func TestLoadConfigRejectsUnknownPolicyValues(t *testing.T) {
	cases := map[string]string{
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
		Commands map[string]any `json:"commands"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if v, ok := saved.Commands["owner_only"]; !ok || v != true {
		t.Errorf("saved commands = %v", saved.Commands)
	}

	// Loading the saved file again changes nothing.
	again, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if again.Channels["telegram"] == nil || !again.Commands.OwnerOnly {
		t.Error("a saved config must load with the same behavior")
	}
}

// A WhatsApp entry an earlier version wrote, bridge or native, loads as the
// WhatsApp channel.
func TestOldWhatsAppEntriesLoad(t *testing.T) {
	cfg, err := LoadConfig(writeConfigFile(t, `{"channel_list":{`+
		`"whatsapp":{"type":"whatsapp","enabled":true,"settings":{"bridge_url":"ws://localhost:3001","use_native":true,"chats":"all"}},`+
		`"phone":{"type":"whatsapp_native","settings":{"session_store_path":"/tmp/wa"}}}}`))
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := cfg.Channels["phone"].Type; got != ChannelWhatsApp {
		t.Fatalf("whatsapp_native entry type = %q, want %q", got, ChannelWhatsApp)
	}
	decoded, err := cfg.Channels["phone"].GetDecoded()
	if err != nil {
		t.Fatalf("GetDecoded: %v", err)
	}
	if s, ok := decoded.(*WhatsAppSettings); !ok || s.SessionStorePath != "/tmp/wa" {
		t.Fatalf("settings = %+v", decoded)
	}
	if !cfg.Channels["whatsapp"].Enabled {
		t.Fatal("the bridge entry lost its enabled state")
	}
}
