package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVoiceConfigSTTViaChatIsAnExplicitOptIn(t *testing.T) {
	if DefaultConfig().Voice.STTViaChat {
		t.Fatal("stt_via_chat defaults on")
	}
	raw, err := json.Marshal(VoiceConfig{STTTarget: "a/b"})
	if err != nil || strings.Contains(string(raw), "stt_via_chat") {
		t.Fatalf("off stt_via_chat marshals as %s (%v)", raw, err)
	}
	var voice VoiceConfig
	if err := json.Unmarshal([]byte(`{"stt_target":"a/chat","stt_via_chat":true}`), &voice); err != nil || !voice.STTViaChat {
		t.Fatalf("stt_via_chat = %#v (%v)", voice, err)
	}
}
