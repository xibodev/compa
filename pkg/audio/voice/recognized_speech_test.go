package voice

import "testing"

func TestRecognizedSpeech(t *testing.T) {
	for transcript, want := range map[string]string{
		"  hello there \n":   "hello there",
		"[laughs] hello":     "[laughs] hello",
		"42":                 "42",
		"こんにちは":              "こんにちは",
		"":                   "",
		"   \t\n":            "",
		"[BLANK_AUDIO]":      "",
		"(silence)":          "",
		"<|nospeech|>":       "",
		"[Music] [Applause]": "",
		"*static*":           "",
		"...":                "",
	} {
		got, recognized := RecognizedSpeech(transcript)
		if got != want || recognized != (want != "") {
			t.Errorf("RecognizedSpeech(%q) = %q, %v; want %q, %v", transcript, got, recognized, want, want != "")
		}
	}
}
