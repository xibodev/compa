export type VoiceActivityState = "waiting" | "speaking" | "complete" | "timeout"

export function createVoiceActivityDetector() {
  let noiseFloor = 0.005
  let voicedMs = 0
  let silenceMs = 0

  return (level: number, elapsedMs: number, frameMs: number): VoiceActivityState => {
    if (elapsedMs < 500) {
      noiseFloor = noiseFloor * 0.8 + level * 0.2
      return "waiting"
    }
    const speaking = level >= Math.max(0.012, noiseFloor * 2.5)
    if (speaking) {
      voicedMs += frameMs
      silenceMs = 0
    } else if (voicedMs >= 200) {
      silenceMs += frameMs
    }
    if (voicedMs >= 250 && silenceMs >= 800) return "complete"
    if (elapsedMs >= 60_000 && voicedMs >= 250) return "complete"
    if (elapsedMs >= 12_000 && voicedMs < 250) return "timeout"
    return voicedMs >= 200 ? "speaking" : "waiting"
  }
}
