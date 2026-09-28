package channels

// VoiceCapabilities describes whether ASR (speech-to-text) and TTS (text-to-speech)
// are available for a channel under the current configuration.
type VoiceCapabilities struct {
	ASR bool
	TTS bool
}

// VoiceCapabilityProvider is an optional interface for channels that want to
// explicitly declare their ASR/TTS support.
type VoiceCapabilityProvider interface {
	VoiceCapabilities() VoiceCapabilities
}

// DetectVoiceCapabilities returns ASR/TTS availability for a channel, gated by
// whether providers are configured. Capabilities come from the channel itself:
// a VoiceCapabilityProvider declares them, and any other channel gets TTS only
// when it can send media. The channel name is not consulted.
func DetectVoiceCapabilities(_ string, ch Channel, asrAvailable bool, ttsAvailable bool) VoiceCapabilities {
	if ch == nil {
		return VoiceCapabilities{}
	}

	if vcp, ok := ch.(VoiceCapabilityProvider); ok {
		caps := vcp.VoiceCapabilities()
		if !asrAvailable {
			caps.ASR = false
		}
		if !ttsAvailable {
			caps.TTS = false
		}
		return caps
	}

	caps := VoiceCapabilities{}
	if ttsAvailable {
		if _, ok := ch.(MediaSender); ok {
			caps.TTS = true
		}
	}

	return caps
}
