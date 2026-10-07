import { afterEach, describe, expect, it, vi } from "vitest"

import {
  NoSpeechError,
  transcribeAudioBlob,
  voiceCapabilities,
} from "./voice-client"

describe("voiceCapabilities", () => {
  const config = (enabled: boolean, stt: string, tts: string) => ({
    enabled,
    stt_target: stt,
    tts_target: tts,
  })

  it("offers each direction its target makes possible", () => {
    expect(voiceCapabilities(config(true, "a/stt", "b/tts"))).toEqual({
      dictation: true,
      spokenReplies: true,
    })
    expect(voiceCapabilities(config(true, "", "b/tts"))).toEqual({
      dictation: false,
      spokenReplies: true,
    })
    expect(voiceCapabilities(config(true, "a/stt", " "))).toEqual({
      dictation: true,
      spokenReplies: false,
    })
  })

  it("offers nothing while voice is off", () => {
    expect(voiceCapabilities(config(false, "a/stt", "b/tts"))).toEqual({
      dictation: false,
      spokenReplies: false,
    })
  })
})

describe("transcribeAudioBlob", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("reports a recording without speech as NoSpeechError", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response("No speech was recognized", { status: 422 }),
        ),
      ),
    )
    await expect(transcribeAudioBlob(new Blob(["x"]))).rejects.toBeInstanceOf(
      NoSpeechError,
    )
  })

  it("returns the transcript", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          new Response(JSON.stringify({ ok: true, text: "hello" }), {
            status: 200,
          }),
        ),
      ),
    )
    await expect(transcribeAudioBlob(new Blob(["x"]))).resolves.toBe("hello")
  })
})
