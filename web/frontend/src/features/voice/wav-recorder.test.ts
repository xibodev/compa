import { describe, expect, it } from "vitest"

import { encodeMonoWav } from "./wav-recorder"

describe("encodeMonoWav", () => {
  it("resamples microphone audio to 16 kHz and writes a valid mono WAV header", async () => {
    const input = new Float32Array(48000)
    const blob = encodeMonoWav(input, 48000)
    const buffer = await new Promise<ArrayBuffer>((resolve, reject) => {
      const reader = new FileReader()
      reader.onload = () => resolve(reader.result as ArrayBuffer)
      reader.onerror = () => reject(reader.error)
      reader.readAsArrayBuffer(blob)
    })
    const view = new DataView(buffer)

    expect(blob.type).toBe("audio/wav")
    expect(view.getUint32(24, true)).toBe(16000)
    expect(view.getUint16(22, true)).toBe(1)
    expect(view.getUint16(34, true)).toBe(16)
    expect(view.getUint32(40, true)).toBe(32000)
    expect(blob.size).toBe(32044)
  })
})
