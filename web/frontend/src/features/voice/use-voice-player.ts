import { useEffect, useRef, useState } from "react"

import { synthesizeSpeech } from "./voice-client"

export function useVoicePlayer() {
  const [isPlaying, setIsPlaying] = useState(false)
  const audioRef = useRef<HTMLAudioElement | null>(null)
  const audioURLRef = useRef<string | null>(null)
  const settleRef = useRef<(() => void) | null>(null)
  const generationRef = useRef(0)

  const cleanup = (generation = generationRef.current) => {
    if (generation !== generationRef.current) return
    if (audioURLRef.current) URL.revokeObjectURL(audioURLRef.current)
    audioURLRef.current = null
    audioRef.current = null
    settleRef.current = null
    setIsPlaying(false)
  }

  const speakText = async (text: string) => {
    if (!text.trim()) return

    stopSpeaking()
    const generation = generationRef.current

    try {
      setIsPlaying(true)
      const audioUrl = await synthesizeSpeech(text)
      if (generation !== generationRef.current) {
        URL.revokeObjectURL(audioUrl)
        return
      }
      const audio = new Audio(audioUrl)
      audioRef.current = audio
      audioURLRef.current = audioUrl
      await new Promise<void>((resolve, reject) => {
        settleRef.current = resolve
        audio.onended = () => resolve()
        audio.onerror = () => reject(new Error("Voice playback failed"))
        void audio.play().catch(reject)
      })
    } finally {
      cleanup(generation)
    }
  }

  const stopSpeaking = () => {
    generationRef.current += 1
    if (audioRef.current) {
      audioRef.current.pause()
      audioRef.current.currentTime = 0
    }
    settleRef.current?.()
    cleanup()
  }

  useEffect(
    () => () => {
      audioRef.current?.pause()
      settleRef.current?.()
      if (audioURLRef.current) URL.revokeObjectURL(audioURLRef.current)
    },
    [],
  )

  return {
    isPlaying,
    speakText,
    stopSpeaking,
  }
}
