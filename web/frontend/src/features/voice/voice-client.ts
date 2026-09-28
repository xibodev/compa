import { launcherFetch } from "@/api/http"

/** Push-to-talk ("cascade") or hands-free ("live") voice turns. */
export type VoiceMode = "cascade" | "live"

export interface VoiceConfig {
  enabled: boolean
  mode: VoiceMode
  /** Exact target "instance-id/model-id" that transcribes speech. */
  stt_target: string
  /**
   * Opt-in: stt_target is a chat model that transcribes the audio it is
   * sent, instead of a model serving audio transcriptions.
   */
  stt_via_chat: boolean
  /** Exact target "instance-id/model-id" that synthesizes speech. */
  tts_target: string
  /** Synthesis voice; empty uses the provider's default. */
  tts_voice: string
  /** Reply to channel voice messages with their transcript. */
  echo_transcription: boolean
}

export interface VoiceOption {
  target: string
  label: string
  provider_kind: string
  /** The display name of the instance serving the target, when known. */
  instance_label?: string
}

export interface VoiceOptions {
  /** Models serving audio transcriptions (and chat models with stt_via_chat). */
  stt: VoiceOption[]
  /** Models serving speech synthesis. */
  tts: VoiceOption[]
  /** Chat models that accept audio, which transcribe when stt_via_chat is on. */
  stt_chat: VoiceOption[]
}

/** What the voice config lets Chat do. */
export interface VoiceCapabilities {
  /** Record speech and turn it into a message. */
  dictation: boolean
  /** Speak replies aloud. */
  spokenReplies: boolean
}

export function voiceCapabilities(
  config: Pick<VoiceConfig, "enabled" | "stt_target" | "tts_target">,
): VoiceCapabilities {
  return {
    dictation: config.enabled && config.stt_target.trim() !== "",
    spokenReplies: config.enabled && config.tts_target.trim() !== "",
  }
}

/** The recording held no speech the model recognized. */
export class NoSpeechError extends Error {
  constructor(message = "No speech was recognized") {
    super(message)
    this.name = "NoSpeechError"
  }
}

/** The server's error message, or fallback with the status. */
async function responseError(resp: Response, fallback: string) {
  const detail = (await resp.text().catch(() => "")).trim()
  return new Error(detail || `${fallback}: ${resp.status}`)
}

export async function fetchVoiceOptions(): Promise<VoiceOptions> {
  const resp = await launcherFetch("/api/voice/options")
  if (!resp.ok) throw await responseError(resp, "Failed to fetch voice options")
  return resp.json()
}

export async function fetchVoiceConfig(): Promise<VoiceConfig> {
  const resp = await launcherFetch("/api/voice/config")
  if (!resp.ok) throw await responseError(resp, "Failed to fetch voice config")
  const data = await resp.json()
  return data.config
}

export async function updateVoiceConfig(
  cfg: VoiceConfig,
): Promise<VoiceConfig> {
  const body: VoiceConfig = {
    enabled: cfg.enabled,
    mode: cfg.mode,
    stt_target: cfg.stt_target.trim(),
    stt_via_chat: cfg.stt_via_chat,
    tts_target: cfg.tts_target.trim(),
    tts_voice: cfg.tts_voice.trim(),
    echo_transcription: cfg.echo_transcription,
  }
  const resp = await launcherFetch("/api/voice/config", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  })
  if (!resp.ok) throw await responseError(resp, "Failed to update voice config")
  const data = await resp.json()
  return data.config
}

export async function transcribeAudioBlob(
  blob: Blob,
  signal?: AbortSignal,
): Promise<string> {
  const formData = new FormData()
  formData.append("file", blob, "recording.wav")
  const resp = await launcherFetch("/api/voice/transcribe", {
    method: "POST",
    body: formData,
    signal,
  })
  // 422 is the server saying the recording held no recognizable speech.
  if (resp.status === 422) {
    const detail = (await resp.text().catch(() => "")).trim()
    throw new NoSpeechError(detail || undefined)
  }
  if (!resp.ok) throw await responseError(resp, "Transcription failed")
  const data = await resp.json()
  return data.text || ""
}

/** Synthesizes text with the configured voice; returns an object URL of the audio. */
export async function synthesizeSpeech(text: string): Promise<string> {
  const resp = await launcherFetch("/api/voice/synthesize", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ input: text }),
  })
  if (!resp.ok) throw await responseError(resp, "Speech synthesis failed")
  const audioBlob = await resp.blob()
  return URL.createObjectURL(audioBlob)
}
