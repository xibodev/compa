package config

import (
	"slices"
	"testing"
)

func TestChannelBuildTag(t *testing.T) {
	supported := []string{
		ChannelWeb, ChannelWebClient, ChannelTelegram, ChannelDiscord, ChannelSlack,
		ChannelWhatsApp, ChannelSlackWebHook, ChannelTeamsWebHook,
	}
	paused := []string{
		ChannelDeltaChat, ChannelDingTalk, ChannelFeishu, ChannelIRC, ChannelLINE, ChannelMaixCam,
		ChannelMatrix, ChannelMQTT, ChannelOneBot, ChannelQQ, ChannelVK, ChannelWeCom, ChannelWeixin,
	}
	// Every known type is supported, paused or native WhatsApp.
	if got, want := len(ChannelTypes()), len(supported)+len(paused)+1; got != want {
		t.Fatalf("%d channel types, want %d: %v", got, want, ChannelTypes())
	}

	for _, typ := range supported {
		if tag := ChannelBuildTag(typ); tag != "" || !ChannelInBuild(typ) {
			t.Errorf("%s: build tag %q, in build %v; want every build to include it", typ, tag, ChannelInBuild(typ))
		}
	}
	for _, typ := range paused {
		if tag := ChannelBuildTag(typ); tag != BuildTagPausedChannels || ChannelInBuild(typ) != PausedChannelsInBuild {
			t.Errorf("%s: build tag %q, in build %v; want %s, %v", typ, tag, ChannelInBuild(typ),
				BuildTagPausedChannels, PausedChannelsInBuild)
		}
	}
	if tag := ChannelBuildTag(ChannelWhatsAppNative); tag != BuildTagWhatsAppNative ||
		ChannelInBuild(ChannelWhatsAppNative) != WhatsAppNativeInBuild {
		t.Errorf("whatsapp_native: build tag %q, in build %v", tag, ChannelInBuild(ChannelWhatsAppNative))
	}
	// A type a program registers itself is in every build.
	if tag := ChannelBuildTag("my_channel"); tag != "" || !ChannelInBuild("my_channel") {
		t.Errorf("my_channel: build tag %q, in build %v", tag, ChannelInBuild("my_channel"))
	}
}

func TestChannelTypesIncludesRegisteredSettings(t *testing.T) {
	RegisterChannelSettings("zz_test_channel", WebChatSettings{})
	t.Cleanup(func() {
		channelSettingsMu.Lock()
		delete(channelSettingsFactory, "zz_test_channel")
		channelSettingsMu.Unlock()
	})
	types := ChannelTypes()
	if !slices.Contains(types, "zz_test_channel") || !slices.IsSorted(types) {
		t.Fatalf("ChannelTypes() = %v", types)
	}
}
