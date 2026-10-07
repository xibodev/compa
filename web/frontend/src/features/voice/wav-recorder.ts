const OUTPUT_SAMPLE_RATE = 16000

type WebkitWindow = Window &
  typeof globalThis & {
    webkitAudioContext?: typeof AudioContext
  }

export interface WavRecorderController {
  stop: () => Promise<Blob>
  cancel: () => void
  getVolume: () => number
  getAudioLevel: () => number
  hasSpeech: () => boolean
}

export async function startWavRecording(): Promise<WavRecorderController> {
  if (!navigator.mediaDevices || !navigator.mediaDevices.getUserMedia) {
    const isLanHttp =
      window.location.protocol === "http:" &&
      window.location.hostname !== "localhost" &&
      window.location.hostname !== "127.0.0.1"
    if (isLanHttp) {
      throw new Error(
        "Microphone access is blocked by browsers on LAN HTTP (" +
          window.location.hostname +
          "). Please open Compa through localhost or configure HTTPS.",
      )
    }
    throw new Error(
      "Microphone API is not supported or permission was blocked by your browser.",
    )
  }

  const stream = await navigator.mediaDevices.getUserMedia({
    audio: {
      channelCount: 1,
      sampleRate: 16000,
      echoCancellation: true,
      noiseSuppression: true,
      autoGainControl: true,
    },
  })

  const AudioContextClass =
    window.AudioContext || (window as WebkitWindow).webkitAudioContext
  if (!AudioContextClass) {
    stream.getTracks().forEach((track) => track.stop())
    throw new Error("Web Audio is not supported by your browser.")
  }
  const ctx = new AudioContextClass()

  const source = ctx.createMediaStreamSource(stream)
  const analyser = ctx.createAnalyser()
  analyser.fftSize = 256
  const dataArray = new Uint8Array(analyser.frequencyBinCount)

  const processor = ctx.createScriptProcessor(4096, 1, 1)
  const silentGain = ctx.createGain()
  silentGain.gain.value = 0
  const pcmChunks: Float32Array[] = []
  let totalSamples = 0
  let speechSamples = 0
  let audioLevel = 0

  processor.onaudioprocess = (e) => {
    const input = e.inputBuffer.getChannelData(0)
    const copy = new Float32Array(input.length)
    copy.set(input)
    pcmChunks.push(copy)
    totalSamples += input.length
    let squareSum = 0
    for (const sample of input) squareSum += sample * sample
    audioLevel = Math.sqrt(squareSum / input.length)
    if (audioLevel >= 0.01) {
      speechSamples += input.length
    }
  }

  source.connect(analyser)
  analyser.connect(processor)
  processor.connect(silentGain)
  silentGain.connect(ctx.destination)

  const getVolume = (): number => {
    analyser.getByteFrequencyData(dataArray)
    let sum = 0
    for (let i = 0; i < dataArray.length; i++) {
      sum += dataArray[i]
    }
    const avg = sum / dataArray.length
    return Math.min(100, Math.round((avg / 128) * 100))
  }

  const cancel = () => {
    processor.disconnect()
    silentGain.disconnect()
    analyser.disconnect()
    source.disconnect()
    stream.getTracks().forEach((track) => track.stop())
    ctx.close().catch(() => {})
  }

  const stop = async (): Promise<Blob> => {
    processor.disconnect()
    silentGain.disconnect()
    analyser.disconnect()
    source.disconnect()
    stream.getTracks().forEach((track) => track.stop())
    await ctx.close().catch(() => {})

    // Flatten float32 samples
    const merged = new Float32Array(totalSamples)
    let offset = 0
    for (const chunk of pcmChunks) {
      merged.set(chunk, offset)
      offset += chunk.length
    }

    return encodeMonoWav(merged, ctx.sampleRate, OUTPUT_SAMPLE_RATE)
  }

  const hasSpeech = () => speechSamples >= ctx.sampleRate / 4
  const getAudioLevel = () => audioLevel

  return { stop, cancel, getVolume, getAudioLevel, hasSpeech }
}

export function encodeMonoWav(
  samples: Float32Array,
  inputSampleRate: number,
  outputSampleRate = OUTPUT_SAMPLE_RATE,
): Blob {
  const resampled = resampleLinear(samples, inputSampleRate, outputSampleRate)
  const buffer = new ArrayBuffer(44 + resampled.length * 2)
  const view = new DataView(buffer)

  writeString(view, 0, "RIFF")
  view.setUint32(4, 36 + resampled.length * 2, true)
  writeString(view, 8, "WAVE")

  writeString(view, 12, "fmt ")
  view.setUint32(16, 16, true)
  view.setUint16(20, 1, true)
  view.setUint16(22, 1, true)
  view.setUint32(24, outputSampleRate, true)
  view.setUint32(28, outputSampleRate * 2, true)
  view.setUint16(32, 2, true)
  view.setUint16(34, 16, true)

  writeString(view, 36, "data")
  view.setUint32(40, resampled.length * 2, true)

  let byteOffset = 44
  for (let i = 0; i < resampled.length; i++) {
    const sample = Math.max(-1, Math.min(1, resampled[i]))
    view.setInt16(
      byteOffset,
      sample < 0 ? sample * 0x8000 : sample * 0x7fff,
      true,
    )
    byteOffset += 2
  }

  return new Blob([view], { type: "audio/wav" })
}

function resampleLinear(
  samples: Float32Array,
  inputSampleRate: number,
  outputSampleRate: number,
): Float32Array {
  if (samples.length === 0 || inputSampleRate === outputSampleRate)
    return samples
  const outputLength = Math.max(
    1,
    Math.round((samples.length * outputSampleRate) / inputSampleRate),
  )
  const output = new Float32Array(outputLength)
  const ratio = inputSampleRate / outputSampleRate
  for (let i = 0; i < outputLength; i++) {
    const position = i * ratio
    const left = Math.floor(position)
    const right = Math.min(left + 1, samples.length - 1)
    const mix = position - left
    output[i] = samples[left] * (1 - mix) + samples[right] * mix
  }
  return output
}

function writeString(view: DataView, offset: number, string: string) {
  for (let i = 0; i < string.length; i++) {
    view.setUint8(offset + i, string.charCodeAt(i))
  }
}
