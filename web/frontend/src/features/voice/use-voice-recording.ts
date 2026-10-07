import { useEffect, useRef, useState } from "react"

import { createVoiceActivityDetector } from "./voice-activity"
import { transcribeAudioBlob } from "./voice-client"
import { type WavRecorderController, startWavRecording } from "./wav-recorder"

export function useVoiceRecording() {
  const [isRecording, setIsRecording] = useState(false)
  const [isTranscribing, setIsTranscribing] = useState(false)
  const [recordingSeconds, setRecordingSeconds] = useState(0)
  const [volume, setVolume] = useState(0)
  const [error, setError] = useState<string | null>(null)

  const controllerRef = useRef<WavRecorderController | null>(null)
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const animRef = useRef<number | null>(null)
  const lastVolumeUpdateRef = useRef(0)
  const lastVolumeRef = useRef(0)
  const recordingGenerationRef = useRef(0)
  const transcriptionAbortRef = useRef<AbortController | null>(null)

  const errorMessage = (cause: unknown, fallback: string) =>
    cause instanceof Error ? cause.message : fallback

  const startRecording = async () => {
    if (controllerRef.current) return
    const generation = ++recordingGenerationRef.current
    try {
      setError(null)
      setRecordingSeconds(0)
      setVolume(0)

      const controller = await startWavRecording()
      if (generation !== recordingGenerationRef.current) {
        controller.cancel()
        return
      }
      controllerRef.current = controller
      setIsRecording(true)

      // Start elapsed timer
      const startTime = Date.now()
      timerRef.current = setInterval(() => {
        setRecordingSeconds(Math.floor((Date.now() - startTime) / 1000))
      }, 500)

      // Start volume poll
      const pollVolume = () => {
        if (!controllerRef.current) return
        const now = performance.now()
        const nextVolume = controller.getVolume()
        if (
          now - lastVolumeUpdateRef.current >= 75 &&
          Math.abs(nextVolume - lastVolumeRef.current) >= 2
        ) {
          lastVolumeUpdateRef.current = now
          lastVolumeRef.current = nextVolume
          setVolume(nextVolume)
        }
        animRef.current = requestAnimationFrame(pollVolume)
      }
      animRef.current = requestAnimationFrame(pollVolume)
    } catch (err: unknown) {
      const msg = errorMessage(err, "Failed to access microphone")
      setError(msg)
      setIsRecording(false)
      throw err
    }
  }

  const cleanup = () => {
    if (timerRef.current) {
      clearInterval(timerRef.current)
      timerRef.current = null
    }
    if (animRef.current) {
      cancelAnimationFrame(animRef.current)
      animRef.current = null
    }
    setVolume(0)
    lastVolumeRef.current = 0
    lastVolumeUpdateRef.current = 0
  }

  const stopRecording = async (): Promise<string> => {
    cleanup()
    const controller = controllerRef.current
    controllerRef.current = null

    if (!controller) {
      setIsRecording(false)
      return ""
    }

    try {
      setIsRecording(false)
      setIsTranscribing(true)

      if (!controller.hasSpeech()) {
        controller.cancel()
        setIsTranscribing(false)
        return ""
      }
      const wavBlob = await controller.stop()
      if (wavBlob.size <= 44) {
        setIsTranscribing(false)
        return ""
      }

      const abort = new AbortController()
      transcriptionAbortRef.current = abort
      const text = await transcribeAudioBlob(wavBlob, abort.signal)
      transcriptionAbortRef.current = null
      setIsTranscribing(false)
      return text
    } catch (err: unknown) {
      setIsTranscribing(false)
      setError(errorMessage(err, "Transcription failed"))
      throw err
    }
  }

  const cancelRecording = () => {
    recordingGenerationRef.current += 1
    transcriptionAbortRef.current?.abort()
    transcriptionAbortRef.current = null
    cleanup()
    if (controllerRef.current) {
      controllerRef.current.cancel()
      controllerRef.current = null
    }
    setIsRecording(false)
    setIsTranscribing(false)
    setRecordingSeconds(0)
  }

  const captureUtterance = async (): Promise<string> => {
    await startRecording()
    const controller = controllerRef.current
    if (!controller) return ""
    const detect = createVoiceActivityDetector()
    const startedAt = performance.now()
    let previousAt = startedAt
    while (controllerRef.current === controller) {
      await new Promise((resolve) => window.setTimeout(resolve, 50))
      const now = performance.now()
      const state = detect(
        controller.getAudioLevel(),
        now - startedAt,
        now - previousAt,
      )
      previousAt = now
      if (state === "complete") return stopRecording()
      if (state === "timeout") {
        cancelRecording()
        return ""
      }
    }
    return ""
  }

  useEffect(() => {
    return () => {
      recordingGenerationRef.current += 1
      transcriptionAbortRef.current?.abort()
      cleanup()
      if (controllerRef.current) {
        controllerRef.current.cancel()
        controllerRef.current = null
      }
    }
  }, [])

  return {
    isRecording,
    isTranscribing,
    recordingSeconds,
    volume,
    error,
    startRecording,
    stopRecording,
    cancelRecording,
    captureUtterance,
  }
}
