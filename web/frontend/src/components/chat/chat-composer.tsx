import {
  IconArrowUp,
  IconLoader2,
  IconMicrophone,
  IconPhotoPlus,
  IconPlayerStop,
  IconRadio,
  IconVolume,
  IconVolumeOff,
  IconX,
} from "@tabler/icons-react"
import {
  type ClipboardEvent as ReactClipboardEvent,
  type DragEvent as ReactDragEvent,
  type KeyboardEvent as ReactKeyboardEvent,
  useRef,
} from "react"
import { useTranslation } from "react-i18next"
import TextareaAutosize from "react-textarea-autosize"

import { ContextUsageRing } from "@/components/chat/context-usage-ring"
import { Button } from "@/components/ui/button"
import { cn } from "@/lib/utils"
import type { ChatAttachment, ContextUsage } from "@/store/chat"

export type ChatInputDisabledReason =
  | "gatewayUnknown"
  | "gatewayStarting"
  | "gatewayRestarting"
  | "gatewayStopping"
  | "gatewayStopped"
  | "gatewayError"
  | "websocketConnecting"
  | "websocketDisconnected"
  | "websocketError"
  | "modelLoading"
  | "modelLoadError"
  | "invalidSelection"
  | "noModelAvailable"

export type LiveVoiceStatus =
  "idle" | "listening" | "transcribing" | "waiting" | "speaking"

interface ChatComposerProps {
  input: string
  attachments: ChatAttachment[]
  onInputChange: (value: string) => void
  onAddImages: () => void
  onPaste: (event: ReactClipboardEvent<HTMLTextAreaElement>) => void
  onDragEnter: (event: ReactDragEvent<HTMLDivElement>) => void
  onDragLeave: (event: ReactDragEvent<HTMLDivElement>) => void
  onDragOver: (event: ReactDragEvent<HTMLDivElement>) => void
  onDrop: (event: ReactDragEvent<HTMLDivElement>) => void
  onRemoveAttachment: (index: number) => void
  onSend: () => void
  onContextDetail?: () => void
  inputDisabledReason: ChatInputDisabledReason | null
  canSend: boolean
  isDragActive: boolean
  contextUsage?: ContextUsage
  /** The voice config transcribes speech: the microphone shows. */
  canDictate?: boolean
  /** The voice config speaks replies: the spoken-replies toggle shows. */
  canSpeak?: boolean
  voiceMode?: "cascade" | "live"
  onVoiceModeChange?: (mode: "cascade" | "live") => void
  isRecording?: boolean
  isTranscribing?: boolean
  recordingSeconds?: number
  volume?: number
  isLiveConnected?: boolean
  liveStatus?: LiveVoiceStatus
  onToggleRecord?: () => void
  onCancelRecord?: () => void
  onToggleLive?: () => void
  isAutoSpeak?: boolean
  onToggleAutoSpeak?: () => void
  /** Voice cannot be used right now, e.g. while Chat is unavailable. */
  voiceDisabled?: boolean
}

export function ChatComposer({
  input,
  attachments,
  onInputChange,
  onAddImages,
  onPaste,
  onDragEnter,
  onDragLeave,
  onDragOver,
  onDrop,
  onRemoveAttachment,
  onSend,
  onContextDetail,
  inputDisabledReason,
  canSend,
  isDragActive,
  contextUsage,
  canDictate = false,
  canSpeak = false,
  voiceMode = "cascade",
  onVoiceModeChange,
  isRecording = false,
  isTranscribing = false,
  recordingSeconds = 0,
  volume = 0,
  isLiveConnected = false,
  liveStatus = "idle",
  onToggleRecord,
  onCancelRecord,
  onToggleLive,
  isAutoSpeak = false,
  onToggleAutoSpeak,
  voiceDisabled = false,
}: ChatComposerProps) {
  const { t } = useTranslation()
  const canInput = inputDisabledReason === null
  const composingRef = useRef(false)
  const hasInput = input.trim().length > 0
  // "No model" reads the same here as in the empty chat above it.
  const disabledMessage =
    inputDisabledReason === null
      ? null
      : inputDisabledReason === "noModelAvailable"
        ? t("chat.empty.noModelDescription")
        : t(`chat.disabledPlaceholder.${inputDisabledReason}`)
  const placeholder = disabledMessage ?? t("chat.placeholder")
  // Hands-free listens and answers aloud, so it needs both directions.
  const canHandsFree = canDictate && canSpeak
  const mode = canHandsFree ? voiceMode : "cascade"
  const autoSpeakLabel = isAutoSpeak
    ? t("chat.voice.autoSpeakOn")
    : t("chat.voice.autoSpeakOff")
  const recordLabel = isRecording
    ? t("chat.voice.stopAndTranscribe")
    : t("chat.voice.speak")

  const handleKeyDown = (e: ReactKeyboardEvent<HTMLTextAreaElement>) => {
    const nativeEvent = e.nativeEvent as Event & {
      isComposing?: boolean
      keyCode?: number
    }
    if (
      composingRef.current ||
      nativeEvent.isComposing ||
      nativeEvent.keyCode === 229
    ) {
      return
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault()
      onSend()
    }
  }

  return (
    <div className="before:bg-background pointer-events-none relative z-10 -mt-[24px] shrink-0 [scrollbar-gutter:stable] overflow-y-auto px-4 pb-[calc(1rem+env(safe-area-inset-bottom))] before:pointer-events-none before:absolute before:inset-x-0 before:top-[24px] before:bottom-0 before:content-[''] md:px-8 md:pb-8 lg:px-24 xl:px-48">
      <div className="pointer-events-auto mx-auto flex max-w-[1000px] flex-col items-end">
        <div
          className={cn(
            "bg-card border-border/60 relative flex w-full flex-col rounded-2xl border p-3 shadow-sm transition-colors",
            isDragActive && "border-violet-400/70 bg-violet-500/5",
          )}
          onDragEnter={onDragEnter}
          onDragLeave={onDragLeave}
          onDragOver={onDragOver}
          onDrop={onDrop}
        >
          {isDragActive && (
            <div className="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-2xl border-2 border-dashed border-violet-400/70 bg-violet-500/10">
              <div className="bg-background/95 text-foreground rounded-full px-4 py-2 text-sm font-medium shadow-sm">
                {t("chat.dropImagesActive")}
              </div>
            </div>
          )}

          {attachments.length > 0 && (
            <div className="mb-3 flex flex-wrap gap-2 px-2">
              {attachments.map((attachment, index) => (
                <div
                  key={`${attachment.url}-${index}`}
                  className="bg-background relative h-20 w-20 overflow-hidden rounded-xl border"
                >
                  <img
                    src={attachment.url}
                    alt={attachment.filename || t("chat.uploadedImage")}
                    className="h-full w-full object-cover"
                  />
                  <button
                    type="button"
                    onClick={() => onRemoveAttachment(index)}
                    className="bg-background/85 text-foreground absolute top-1 right-1 inline-flex h-6 w-6 items-center justify-center rounded-full border shadow-sm transition hover:bg-white"
                    aria-label={t("chat.removeImage")}
                    title={t("chat.removeImage")}
                  >
                    <IconX className="h-3.5 w-3.5" />
                  </button>
                </div>
              ))}
            </div>
          )}

          {isRecording && (
            <div className="mb-2 flex flex-wrap items-center justify-between gap-2 rounded-xl border border-red-500/30 bg-red-500/10 px-3.5 py-2">
              <div className="flex min-w-0 items-center gap-2.5">
                <span className="relative flex h-2.5 w-2.5 shrink-0">
                  <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-red-400 opacity-75" />
                  <span className="relative inline-flex h-2.5 w-2.5 rounded-full bg-red-500" />
                </span>
                <span className="text-xs font-semibold text-red-600 tabular-nums dark:text-red-400">
                  {t("chat.voice.recording", { seconds: recordingSeconds })}
                </span>
                <div
                  aria-hidden="true"
                  className="flex h-3 items-center gap-0.5"
                >
                  {[20, 60, 40, 80, 50, 90, 30].map((h, i) => (
                    <span
                      key={i}
                      className="w-0.5 rounded-full bg-red-500 transition-all duration-150"
                      style={{
                        height: `${Math.max(2, Math.min(12, (volume * h) / 100))}px`,
                      }}
                    />
                  ))}
                </div>
              </div>
              <div className="flex items-center gap-1.5">
                {onCancelRecord && (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    className="text-muted-foreground hover:text-foreground h-7 px-2 text-xs"
                    onClick={onCancelRecord}
                  >
                    {t("common.cancel")}
                  </Button>
                )}
                <Button
                  type="button"
                  size="sm"
                  className="h-7 bg-red-600 px-3 text-xs font-semibold text-white hover:bg-red-700"
                  onClick={onToggleRecord}
                >
                  {t("chat.voice.done")}
                </Button>
              </div>
            </div>
          )}

          {isTranscribing && (
            <div
              role="status"
              className="mb-2 flex items-center gap-2 rounded-xl border border-blue-500/30 bg-blue-500/10 px-3.5 py-2 text-blue-600 dark:text-blue-400"
            >
              <IconLoader2 className="size-4 animate-spin" />
              <span className="text-xs font-medium">
                {t("chat.voice.transcribing")}
              </span>
            </div>
          )}

          <TextareaAutosize
            value={input}
            onChange={(e) => onInputChange(e.target.value)}
            onCompositionStart={() => {
              composingRef.current = true
            }}
            onCompositionEnd={() => {
              composingRef.current = false
            }}
            onPaste={onPaste}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            aria-label={t("chat.messageLabel")}
            disabled={!canInput}
            title={disabledMessage || undefined}
            className={cn(
              "placeholder:text-muted-foreground/70 max-h-[200px] min-h-[64px] resize-none border-0 bg-transparent px-2 py-1 text-[15px] shadow-none transition-colors focus-visible:ring-0 focus-visible:outline-none dark:bg-transparent",
              !canInput && "cursor-not-allowed",
            )}
            minRows={1}
            maxRows={8}
          />

          <div className="mt-2 flex items-center justify-between gap-2 px-1">
            <div className="flex min-w-0 items-center gap-1">
              <Button
                type="button"
                variant="ghost"
                size="icon"
                className="text-muted-foreground hover:text-foreground h-8 w-8 rounded-full"
                onClick={onAddImages}
                disabled={!canInput}
                aria-label={t("chat.attachImage")}
                title={t("chat.attachImage")}
              >
                <IconPhotoPlus className="size-4" />
              </Button>

              {mode === "cascade" ? (
                <>
                  {canDictate && (
                    <Button
                      type="button"
                      variant={isRecording ? "destructive" : "ghost"}
                      size="icon"
                      className={cn(
                        "h-8 w-8 rounded-full transition-all",
                        isRecording
                          ? "animate-pulse bg-red-500 text-white"
                          : isTranscribing
                            ? "text-blue-500"
                            : "text-muted-foreground hover:text-foreground",
                      )}
                      onClick={onToggleRecord}
                      disabled={voiceDisabled || !canInput || isTranscribing}
                      aria-label={recordLabel}
                      title={recordLabel}
                    >
                      {isTranscribing ? (
                        <IconLoader2 className="size-4 animate-spin" />
                      ) : isRecording ? (
                        <IconPlayerStop className="size-4" />
                      ) : (
                        <IconMicrophone className="size-4" />
                      )}
                    </Button>
                  )}

                  {canSpeak && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className={cn(
                        "h-8 w-8 rounded-full transition-colors",
                        isAutoSpeak
                          ? "text-violet-500"
                          : "text-muted-foreground hover:text-foreground",
                      )}
                      onClick={onToggleAutoSpeak}
                      aria-pressed={isAutoSpeak}
                      aria-label={t("chat.voice.autoSpeak")}
                      title={autoSpeakLabel}
                    >
                      {isAutoSpeak ? (
                        <IconVolume className="size-4" />
                      ) : (
                        <IconVolumeOff className="size-4" />
                      )}
                    </Button>
                  )}
                </>
              ) : (
                <Button
                  type="button"
                  variant={isLiveConnected ? "destructive" : "secondary"}
                  size="sm"
                  className={cn(
                    "h-8 gap-1.5 rounded-full px-3 text-xs font-medium",
                    isLiveConnected
                      ? "animate-pulse bg-emerald-600 text-white hover:bg-emerald-700"
                      : "bg-muted text-foreground hover:bg-muted/80",
                  )}
                  onClick={onToggleLive}
                  disabled={voiceDisabled && !isLiveConnected}
                >
                  <IconRadio className="size-3.5" />
                  {isLiveConnected
                    ? t("chat.voice.stopHandsFree", {
                        status: t(`chat.voice.liveStatus.${liveStatus}`),
                      })
                    : t("chat.voice.startHandsFree")}
                </Button>
              )}

              {canHandsFree && onVoiceModeChange && (
                <button
                  type="button"
                  onClick={() =>
                    onVoiceModeChange(
                      voiceMode === "cascade" ? "live" : "cascade",
                    )
                  }
                  disabled={isLiveConnected}
                  className="border-border/60 text-muted-foreground hover:text-foreground hover:bg-muted/50 ml-1 truncate rounded-full border px-2 py-0.5 text-[11px] font-medium transition-colors"
                  title={t("chat.voice.switchMode")}
                >
                  {voiceMode === "cascade"
                    ? t("chat.voice.pushToTalk")
                    : t("chat.voice.handsFree")}
                </button>
              )}
            </div>

            <div className="flex shrink-0 items-center gap-1.5">
              {contextUsage && (
                <ContextUsageRing
                  usage={contextUsage}
                  onDetailClick={onContextDetail}
                />
              )}
              {canInput ? (
                <span tabIndex={!canSend ? 0 : undefined}>
                  <Button
                    type="button"
                    size="icon"
                    className="size-8 rounded-full bg-violet-500 text-white transition-transform hover:bg-violet-600 active:scale-95"
                    onClick={onSend}
                    disabled={!canSend}
                    aria-label={t("chat.sendMessage")}
                  >
                    <IconArrowUp className="size-4" />
                  </Button>
                </span>
              ) : null}
            </div>
          </div>
        </div>

        <div
          aria-hidden={!hasInput}
          className={cn(
            "border-border/50 bg-muted/55 text-muted-foreground dark:bg-muted/45 mt-2 inline-flex items-center rounded-md border px-3 py-1 text-[11px] shadow-sm transition-all duration-200",
            hasInput
              ? "translate-y-0 opacity-100"
              : "pointer-events-none -translate-y-1 opacity-0",
          )}
        >
          {t("chat.composeHint")}
        </div>
      </div>
    </div>
  )
}
