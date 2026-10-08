package config

import (
	"slices"
	"sort"
)

// Default builds, and so the releases, include only the supported channels:
// the web chat, Telegram, Discord, Slack, WhatsApp through its bridge, and
// the Slack and Teams webhooks. The other channels are paused: builds made
// with the paused_channels build tag include them, and native WhatsApp needs
// the whatsapp_native tag. Every build loads their config, so a config that
// enables one still works with a build that includes it.
const (
	BuildTagPausedChannels = "paused_channels"
	BuildTagWhatsAppNative = "whatsapp_native"
)

// pausedChannelTypes are the channel types builds include only with the
// paused_channels build tag.
var pausedChannelTypes = []string{
	ChannelDeltaChat,
	ChannelDingTalk,
	ChannelFeishu,
	ChannelIRC,
	ChannelLINE,
	ChannelMaixCam,
	ChannelMatrix,
	ChannelMQTT,
	ChannelOneBot,
	ChannelQQ,
	ChannelVK,
	ChannelWeCom,
	ChannelWeixin,
}

// ChannelBuildTag is the build tag a build needs to include the channel type
// t, or "" when every build includes it.
func ChannelBuildTag(t string) string {
	switch {
	case t == ChannelWhatsAppNative:
		return BuildTagWhatsAppNative
	case slices.Contains(pausedChannelTypes, t):
		return BuildTagPausedChannels
	}
	return ""
}

// ChannelInBuild reports whether this build includes the channel type t: it
// is false only for a channel whose build tag this build was made without.
func ChannelInBuild(t string) bool {
	switch ChannelBuildTag(t) {
	case BuildTagPausedChannels:
		return PausedChannelsInBuild
	case BuildTagWhatsAppNative:
		return WhatsAppNativeInBuild
	}
	return true
}

// ChannelTypes returns the known channel types, sorted, including those
// RegisterChannelSettings added.
func ChannelTypes() []string {
	channelSettingsMu.RLock()
	defer channelSettingsMu.RUnlock()
	types := make([]string, 0, len(channelSettingsFactory))
	for t := range channelSettingsFactory {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
