import { describe, expect, it } from "vitest"

import { createVoiceActivityDetector } from "./voice-activity"

describe("voice activity detector", () => {
  it("completes after sustained speech followed by silence", () => {
    const update = createVoiceActivityDetector()
    for (let elapsed = 0; elapsed < 600; elapsed += 50) update(0.003, elapsed, 50)
    for (let elapsed = 600; elapsed < 1200; elapsed += 50)
      update(0.08, elapsed, 50)
    let state = "waiting"
    for (let elapsed = 1200; elapsed < 2100; elapsed += 50)
      state = update(0.002, elapsed, 50)
    expect(state).toBe("complete")
  })

  it("times out without treating room noise as speech", () => {
    const update = createVoiceActivityDetector()
    let state = "waiting"
    for (let elapsed = 0; elapsed <= 12_000; elapsed += 50)
      state = update(0.004, elapsed, 50)
    expect(state).toBe("timeout")
  })

  it("completes a continuous utterance at the maximum duration", () => {
    const update = createVoiceActivityDetector()
    let state = "waiting"
    for (let elapsed = 0; elapsed <= 60_000; elapsed += 50)
      state = update(elapsed < 600 ? 0.003 : 0.08, elapsed, 50)
    expect(state).toBe("complete")
  })
})
