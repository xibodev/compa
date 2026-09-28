import {
  IconAlertTriangle,
  IconDeviceFloppy,
  IconLoader2,
  IconMicrophone,
  IconPlayerPlay,
  IconPlayerStop,
} from "@tabler/icons-react"
import { useEffect, useEffectEvent, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  listProviderInstances,
  listProviderTargets,
} from "@/api/provider-instances"
import { instanceDisplayName } from "@/components/models/provider-model"
import {
  SearchableSelect,
  type SearchableSelectGroup,
} from "@/components/searchable-select"
import {
  AdvancedSection,
  Field,
  SwitchCardField,
} from "@/components/shared-form"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { useVoiceRecording } from "@/features/voice/use-voice-recording"
import {
  NoSpeechError,
  type VoiceConfig,
  type VoiceMode,
  type VoiceOptions,
  fetchVoiceConfig,
  fetchVoiceOptions,
  synthesizeSpeech,
  updateVoiceConfig,
  voiceCapabilities,
} from "@/features/voice/voice-client"
import {
  type VoiceProviderLabels,
  groupVoiceOptions,
  voiceOptionProvider,
} from "@/lib/voice-options"

const EMPTY: VoiceConfig = {
  enabled: false,
  mode: "cascade",
  stt_target: "",
  stt_via_chat: false,
  tts_target: "",
  tts_voice: "",
  echo_transcription: false,
}

// Target picker values that are not targets: no target, and a target typed
// by hand because no catalog lists it.
const NO_TARGET = "__none__"
const TYPED_TARGET = "__typed__"

type TargetField = "stt_target" | "tts_target"

const NOT_TYPING: Record<TargetField, boolean> = {
  stt_target: false,
  tts_target: false,
}

const NO_OPTIONS: VoiceOptions = { stt: [], tts: [], stt_chat: [] }

const NO_LABELS: VoiceProviderLabels = {
  byTarget: new Map(),
  byInstance: new Map(),
}

const errorText = (cause: unknown, fallback: string) =>
  cause instanceof Error && cause.message ? cause.message : fallback

type PreviewState = "idle" | "loading" | "playing"

/**
 * Voice settings: dictation (speech to text) and spoken replies (text to
 * speech) each work on their own, on the models of connected providers.
 */
export function VoiceSettings() {
  const { t, i18n } = useTranslation()
  const uiLanguage = i18n.resolvedLanguage ?? i18n.language ?? "en"
  const [config, setConfig] = useState<VoiceConfig>(EMPTY)
  const [options, setOptions] = useState<VoiceOptions>(NO_OPTIONS)
  // Until the options load, an empty list says nothing about the providers.
  const [optionsLoaded, setOptionsLoaded] = useState(false)
  const [labels, setLabels] = useState<VoiceProviderLabels>(NO_LABELS)
  const [typing, setTyping] = useState(NOT_TYPING)
  const [saving, setSaving] = useState(false)
  const [preview, setPreview] = useState<PreviewState>("idle")
  const previewAudioRef = useRef<HTMLAudioElement | null>(null)
  const previewURLRef = useRef<string | null>(null)
  const [transcription, setTranscription] = useState("")
  const {
    isRecording,
    isTranscribing,
    recordingSeconds,
    startRecording,
    stopRecording,
    cancelRecording,
  } = useVoiceRecording()

  const reportLoadError = useEffectEvent((error: unknown) =>
    toast.error(errorText(error, t("voice.errors.loadFailed"))),
  )

  useEffect(() => {
    void Promise.all([fetchVoiceConfig(), fetchVoiceOptions()])
      .then(([voice, available]) => {
        setConfig({ ...EMPTY, ...voice })
        setOptions({ ...NO_OPTIONS, ...available })
        setOptionsLoaded(true)
      })
      .catch(reportLoadError)
    // Provider names for the pickers; without them the ids still read.
    void Promise.all([listProviderTargets(true), listProviderInstances()])
      .then(([targets, instances]) =>
        setLabels({
          byTarget: new Map(
            (targets.targets ?? []).flatMap((target) => {
              const name = target.instance_label?.trim()
              return name ? [[target.target, name] as const] : []
            }),
          ),
          byInstance: new Map(
            (instances.instances ?? []).map((instance) => [
              instance.id,
              instanceDisplayName(instance),
            ]),
          ),
        }),
      )
      .catch(() => {})
  }, [])

  const stopPreview = () => {
    previewAudioRef.current?.pause()
    previewAudioRef.current = null
    if (previewURLRef.current) URL.revokeObjectURL(previewURLRef.current)
    previewURLRef.current = null
    setPreview("idle")
  }

  useEffect(
    () => () => {
      previewAudioRef.current?.pause()
      if (previewURLRef.current) URL.revokeObjectURL(previewURLRef.current)
    },
    [],
  )

  const capabilities = voiceCapabilities(config)
  const canHandsFree = capabilities.dictation && capabilities.spokenReplies

  const save = async (quiet = false) => {
    if (
      config.enabled &&
      !config.stt_target.trim() &&
      !config.tts_target.trim()
    ) {
      toast.error(t("voice.errors.nothingToUse"))
      return false
    }
    setSaving(true)
    try {
      // Hands-free needs both directions; without them voice is push-to-talk.
      const updated = await updateVoiceConfig({
        ...config,
        mode: canHandsFree ? config.mode : "cascade",
      })
      setConfig({ ...EMPTY, ...updated })
      setTyping(NOT_TYPING)
      // The options list the configured targets, typed ones included.
      void fetchVoiceOptions()
        .then((available) => setOptions({ ...NO_OPTIONS, ...available }))
        .catch(() => {})
      if (!quiet) toast.success(t("voice.saved"))
      return true
    } catch (error) {
      toast.error(errorText(error, t("voice.errors.saveFailed")))
      return false
    } finally {
      setSaving(false)
    }
  }

  const startPreview = async () => {
    setPreview("loading")
    try {
      if (!(await save(true))) {
        setPreview("idle")
        return
      }
      const url = await synthesizeSpeech(t("voice.previewText"))
      previewURLRef.current = url
      const audio = new Audio(url)
      previewAudioRef.current = audio
      audio.onended = () => stopPreview()
      audio.onerror = () => {
        toast.error(t("voice.errors.playbackFailed"))
        stopPreview()
      }
      await audio.play()
      setPreview("playing")
    } catch (error) {
      toast.error(errorText(error, t("voice.errors.previewFailed")))
      stopPreview()
    }
  }

  const testMicrophone = async () => {
    try {
      if (!isRecording) {
        setTranscription("")
        if (!(await save(true))) return
        await startRecording()
        return
      }
      const text = (await stopRecording()).trim()
      setTranscription(text)
      if (text) toast.success(t("voice.micOk"))
      else toast.info(t("chat.voice.noSpeech"))
    } catch (error) {
      if (error instanceof NoSpeechError) {
        toast.info(t("chat.voice.noSpeech"))
        return
      }
      toast.error(errorText(error, t("voice.errors.micFailed")))
    }
  }

  const targetPicker = (field: TargetField, label: string, hint: string) => {
    // With stt_via_chat the speech-to-text target is a chat model.
    const available =
      field === "tts_target"
        ? options.tts
        : config.stt_via_chat
          ? options.stt_chat
          : options.stt
    const value = config[field]
    const listed = available.some((option) => option.target === value)
    const selected =
      typing[field] || (value !== "" && !listed)
        ? TYPED_TARGET
        : value || NO_TARGET
    const setTarget = (target: string) =>
      setConfig((current) => ({ ...current, [field]: target }))
    const providerOf = (option: (typeof available)[number]) =>
      voiceOptionProvider(option, labels)
    const groups: SearchableSelectGroup[] = groupVoiceOptions(
      available,
      uiLanguage,
      providerOf,
    ).map((group) => ({
      key: group.key,
      label: group.label,
      options: group.options.map((option) => ({
        value: option.target,
        label: option.label,
        // A language group names no provider, so each voice does.
        description: group.key.startsWith("locale:")
          ? providerOf(option)
          : undefined,
        keywords: option.target,
      })),
    }))
    return (
      <Field label={label} hint={hint}>
        <SearchableSelect
          label={label}
          value={selected}
          onValueChange={(next) => {
            setTyping((current) => ({
              ...current,
              [field]: next === TYPED_TARGET,
            }))
            if (next === NO_TARGET) setTarget("")
            else if (next !== TYPED_TARGET) setTarget(next)
          }}
          groups={groups}
          leading={[{ value: NO_TARGET, label: t("voice.none") }]}
          trailing={[{ value: TYPED_TARGET, label: t("voice.otherTarget") }]}
          placeholder={t("voice.none")}
          searchPlaceholder={
            field === "tts_target"
              ? t("voice.searchVoices")
              : t("voice.searchModels")
          }
          emptyText={t("voice.noMatch")}
        />
        {selected === TYPED_TARGET && (
          <Input
            value={value}
            onChange={(event) => setTarget(event.target.value)}
            placeholder="instance-id/model-id"
            aria-label={t("voice.targetInput", { label })}
            spellCheck={false}
          />
        )}
      </Field>
    )
  }

  return (
    <div className="space-y-6">
      <Card size="sm">
        <CardHeader className="border-border border-b">
          <CardTitle>{t("voice.chat.title")}</CardTitle>
          <CardDescription>{t("voice.chat.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4 pt-4">
          <SwitchCardField
            label={t("voice.enable")}
            hint={t("voice.enableHint")}
            checked={config.enabled}
            onCheckedChange={(enabled) =>
              setConfig((current) => ({ ...current, enabled }))
            }
          />
          {config.enabled && (
            <p className="text-muted-foreground text-xs" role="status">
              {capabilities.dictation && capabilities.spokenReplies
                ? t("voice.summary.both")
                : capabilities.dictation
                  ? t("voice.summary.dictation")
                  : capabilities.spokenReplies
                    ? t("voice.summary.spokenReplies")
                    : t("voice.summary.none")}
            </p>
          )}
          <Field
            label={t("voice.mode")}
            hint={canHandsFree ? t("voice.modeHint") : t("voice.modeNeedsBoth")}
          >
            <Select
              value={canHandsFree ? config.mode : "cascade"}
              disabled={!canHandsFree}
              onValueChange={(mode) =>
                setConfig((current) => ({
                  ...current,
                  mode: mode as VoiceMode,
                }))
              }
            >
              <SelectTrigger className="h-9" aria-label={t("voice.mode")}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="cascade">
                  {t("chat.voice.pushToTalk")}
                </SelectItem>
                <SelectItem value="live">{t("voice.handsFree")}</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </CardContent>
      </Card>

      <Card size="sm">
        <CardHeader className="border-border border-b">
          <CardTitle>{t("voice.stt.title")}</CardTitle>
          <CardDescription>{t("voice.stt.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4 pt-4">
          <SwitchCardField
            label={t("voice.sttViaChat")}
            hint={t("voice.sttViaChatHint")}
            checked={config.stt_via_chat}
            onCheckedChange={(stt_via_chat) =>
              setConfig((current) => ({ ...current, stt_via_chat }))
            }
          />
          {config.stt_via_chat && options.stt_chat.length === 0 && (
            <p
              role="note"
              className="flex items-start gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
            >
              <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
              {t("voice.sttChatEmpty")}
            </p>
          )}
          {targetPicker(
            "stt_target",
            t("voice.sttModel"),
            t("voice.sttModelHint"),
          )}
          {optionsLoaded &&
            !config.stt_via_chat &&
            options.stt.length === 0 && (
              <p
                role="note"
                className="flex items-start gap-2 rounded-lg bg-amber-500/10 px-3 py-2 text-xs text-amber-700 dark:text-amber-300"
              >
                <IconAlertTriangle className="mt-0.5 size-4 shrink-0" />
                {t("voice.sttEmpty", { option: t("voice.sttViaChat") })}
              </p>
            )}
          <div className="flex flex-wrap items-center gap-2">
            {/* Testing needs a model, not voice turned on for Chat. */}
            <Button
              type="button"
              variant="outline"
              disabled={isTranscribing || !config.stt_target.trim()}
              onClick={() => void testMicrophone()}
            >
              {isRecording ? (
                <IconPlayerStop className="size-4" />
              ) : isTranscribing ? (
                <IconLoader2 className="size-4 animate-spin" />
              ) : (
                <IconMicrophone className="size-4" />
              )}
              {isRecording
                ? t("voice.micStop", { seconds: recordingSeconds })
                : isTranscribing
                  ? t("chat.voice.transcribing")
                  : t("voice.micTest")}
            </Button>
            {isRecording && (
              <Button type="button" variant="ghost" onClick={cancelRecording}>
                {t("common.cancel")}
              </Button>
            )}
            {transcription && (
              <p className="text-muted-foreground text-xs">
                {t("voice.heard", { text: transcription })}
              </p>
            )}
          </div>
        </CardContent>
      </Card>

      <Card size="sm">
        <CardHeader className="border-border border-b">
          <CardTitle>{t("voice.tts.title")}</CardTitle>
          <CardDescription>{t("voice.tts.description")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4 pt-4">
          {targetPicker(
            "tts_target",
            t("voice.ttsModel"),
            t("voice.ttsModelHint"),
          )}
          <div className="flex flex-wrap items-center gap-2">
            {preview === "playing" ? (
              <Button type="button" variant="outline" onClick={stopPreview}>
                <IconPlayerStop className="size-4" />
                {t("voice.previewStop")}
              </Button>
            ) : (
              <Button
                type="button"
                variant="outline"
                disabled={preview === "loading" || !config.tts_target.trim()}
                onClick={() => void startPreview()}
              >
                {preview === "loading" ? (
                  <IconLoader2 className="size-4 animate-spin" />
                ) : (
                  <IconPlayerPlay className="size-4" />
                )}
                {preview === "loading"
                  ? t("voice.previewLoading")
                  : t("voice.preview")}
              </Button>
            )}
            {preview === "playing" && (
              <span role="status" className="text-muted-foreground text-xs">
                {t("voice.previewPlaying")}
              </span>
            )}
          </div>
          <AdvancedSection>
            <Field
              label={t("voice.voiceName")}
              hint={t("voice.voiceNameHint")}
              htmlFor="voice-name"
            >
              <Input
                id="voice-name"
                value={config.tts_voice}
                onChange={(event) =>
                  setConfig((current) => ({
                    ...current,
                    tts_voice: event.target.value,
                  }))
                }
                placeholder={t("voice.voiceNamePlaceholder")}
                spellCheck={false}
              />
            </Field>
          </AdvancedSection>
        </CardContent>
      </Card>

      <Card size="sm">
        <CardHeader className="border-border border-b">
          <CardTitle>{t("voice.channels.title")}</CardTitle>
          <CardDescription>{t("voice.channels.description")}</CardDescription>
        </CardHeader>
        <CardContent className="pt-4">
          <SwitchCardField
            label={t("voice.echo")}
            hint={t("voice.echoHint")}
            checked={config.echo_transcription}
            onCheckedChange={(echo_transcription) =>
              setConfig((current) => ({ ...current, echo_transcription }))
            }
          />
        </CardContent>
      </Card>

      <div className="flex justify-end">
        <Button type="button" disabled={saving} onClick={() => void save()}>
          <IconDeviceFloppy className="size-4" />
          {saving ? t("common.saving") : t("voice.save")}
        </Button>
      </div>
    </div>
  )
}
