import { IconAdjustmentsHorizontal, IconPlus } from "@tabler/icons-react"
import { useAtom } from "jotai"
import {
  type ChangeEvent,
  type ClipboardEvent,
  type DragEvent,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
} from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { AssistantMessage } from "@/components/chat/assistant-message"
import {
  ChatComposer,
  type ChatInputDisabledReason,
  type LiveVoiceStatus,
} from "@/components/chat/chat-composer"
import { ChatEmptyState } from "@/components/chat/chat-empty-state"
import { ModelSelector } from "@/components/chat/model-selector"
import { ModuleSelector, NO_MODULE } from "@/components/chat/module-selector"
import { SessionHistoryMenu } from "@/components/chat/session-history-menu"
import { TypingIndicator } from "@/components/chat/typing-indicator"
import { UserMessage } from "@/components/chat/user-message"
import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import {
  CHAT_IMAGE_ACCEPT,
  buildChatImageAttachments,
  getTransferredFiles,
  hasFileTransfer,
} from "@/features/chat/image-input"
import {
  type ChatModelAvailability,
  resolveChatModelAvailability,
} from "@/features/chat/model-availability"
import { useVoicePlayer } from "@/features/voice/use-voice-player"
import { useVoiceRecording } from "@/features/voice/use-voice-recording"
import {
  NoSpeechError,
  type VoiceCapabilities,
  fetchVoiceConfig,
  voiceCapabilities,
} from "@/features/voice/voice-client"
import { useChatSelections } from "@/hooks/use-chat-selections"
import { useDefaultModel } from "@/hooks/use-default-model"
import { useGateway } from "@/hooks/use-gateway"
import { useSessionHistory } from "@/hooks/use-session-history"
import { useWebChat } from "@/hooks/use-web-chat"
import { formatModelLabel, selectionLabel } from "@/lib/model-labels"
import type { AssistantDetailVisibility } from "@/store/chat"
import type { ConnectionState } from "@/store/chat"
import type { ChatAttachment } from "@/store/chat"
import {
  assistantDetailVisibilityAtom,
  shouldShowAssistantMessage,
} from "@/store/chat"
import type { GatewayState } from "@/store/gateway"

const NO_VOICE: VoiceCapabilities = { dictation: false, spokenReplies: false }

// Transcripts some models return for audio without speech.
const NON_SPEECH_TRANSCRIPT = /^[[(].*[\])]$/

function resolveChatInputDisabledReason({
  modelAvailability,
  connectionState,
  gatewayState,
}: {
  modelAvailability: ChatModelAvailability
  connectionState: ConnectionState
  gatewayState: GatewayState
}): ChatInputDisabledReason | null {
  if (gatewayState === "unknown") {
    return "gatewayUnknown"
  }

  if (gatewayState === "starting") {
    return "gatewayStarting"
  }

  if (gatewayState === "restarting") {
    return "gatewayRestarting"
  }

  if (gatewayState === "stopping") {
    return "gatewayStopping"
  }

  if (gatewayState === "stopped") {
    return "gatewayStopped"
  }

  if (gatewayState === "error") {
    return "gatewayError"
  }

  if (connectionState === "connecting") {
    return "websocketConnecting"
  }

  if (connectionState === "error") {
    return "websocketError"
  }

  if (connectionState === "disconnected") {
    return "websocketDisconnected"
  }

  // Only a chat with nothing to send with is blocked: without a default
  // model the chat can still pick a target or route in the selector.
  switch (modelAvailability) {
    case "loading":
      return "modelLoading"
    case "loadFailed":
      return "modelLoadError"
    case "invalid":
      return "invalidSelection"
    case "unavailable":
      return "noModelAvailable"
    default:
      return null
  }
}

export function ChatPage() {
  const { t } = useTranslation()
  const scrollRef = useRef<HTMLDivElement>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)
  const dragDepthRef = useRef(0)
  const [isAtBottom, setIsAtBottom] = useState(true)
  const [hasScrolled, setHasScrolled] = useState(false)
  // The module the user has pointed the agent at for this conversation. Empty
  // means none: an installed module costs one line of capability summary until
  // it is selected, and only then does its overlay and skills load.
  const [selectedModule, setSelectedModule] = useState("")
  const [input, setInput] = useState("")
  const [attachments, setAttachments] = useState<ChatAttachment[]>([])
  const [isDragActive, setIsDragActive] = useState(false)
  const [assistantDetailVisibility, setAssistantDetailVisibility] = useAtom(
    assistantDetailVisibilityAtom,
  )

  const assistantDetailVisibilityOptions: Array<{
    value: AssistantDetailVisibility
    label: string
  }> = [
    { value: "none", label: t("chat.assistantDetailVisibility.none") },
    { value: "thought", label: t("chat.assistantDetailVisibility.thought") },
    {
      value: "tool_calls",
      label: t("chat.assistantDetailVisibility.toolCalls"),
    },
    { value: "all", label: t("chat.assistantDetailVisibility.all") },
  ]

  const {
    messages,
    connectionState,
    isTyping,
    activeSessionId,
    contextUsage,
    sendMessage,
    sendVoiceMessage,
    selection,
    setSelection,
    switchSession,
    newChat,
  } = useWebChat()

  const { state: gwState } = useGateway()
  const {
    targets: selectionTargets,
    routes: selectionRoutes,
    loading: selectionsLoading,
    error: selectionsError,
    refresh: refreshSelections,
  } = useChatSelections()

  const defaultModel = useDefaultModel()
  // Null while the default is unknown: not loaded yet, or its load failed.
  const defaultSelection = defaultModel.loaded ? defaultModel.selection : null
  const modelsLoading = selectionsLoading || defaultModel.loading
  const modelAvailability = resolveChatModelAvailability({
    loading: modelsLoading,
    defaultSelection: defaultModel.selection,
    defaultFailed: !defaultModel.loaded && defaultModel.error !== "",
    targets: selectionTargets,
    routes: selectionRoutes,
    selectionsFailed: selectionsError !== "",
    selection,
  })
  const modelLoadError = [selectionsError, defaultModel.error]
    .filter(Boolean)
    .join("; ")
  const retryModelLoad = () =>
    void Promise.all([refreshSelections(), defaultModel.refresh()])
  const inputDisabledReason = resolveChatInputDisabledReason({
    modelAvailability,
    connectionState,
    gatewayState: gwState,
  })
  const canInput = inputDisabledReason === null

  const {
    sessions,
    hasMore,
    loadError,
    loadErrorMessage,
    observerRef,
    loadSessions,
    handleDeleteSession,
  } = useSessionHistory({
    activeSessionId,
    onDeletedActiveSession: newChat,
  })

  const syncScrollState = (element: HTMLDivElement) => {
    const { clientHeight, scrollHeight, scrollTop } = element
    setHasScrolled(scrollTop > 0)
    setIsAtBottom(scrollHeight - scrollTop <= clientHeight + 10)
  }

  const handleScroll = (e: React.UIEvent<HTMLDivElement>) => {
    syncScrollState(e.currentTarget)
  }

  useEffect(() => {
    if (scrollRef.current) {
      if (isAtBottom) {
        scrollRef.current.scrollTop = scrollRef.current.scrollHeight
      }
      syncScrollState(scrollRef.current)
    }
  }, [messages, isTyping, isAtBottom])

  const [voiceMode, setVoiceMode] = useState<"cascade" | "live">("cascade")
  const [voice, setVoice] = useState<VoiceCapabilities>(NO_VOICE)
  const [isAutoSpeak, setIsAutoSpeak] = useState(false)
  const [isHandsFree, setIsHandsFree] = useState(false)
  const [liveStatus, setLiveStatus] = useState<LiveVoiceStatus>("idle")
  const [moduleCount, setModuleCount] = useState(0)
  const handsFreeRef = useRef(false)
  const handsFreeGenerationRef = useRef(0)
  const pendingHandsFreeReplyRef = useRef(false)
  const pendingHandsFreeMessageCountRef = useRef(0)
  const handsFreeReplyTimerRef = useRef<number | null>(null)
  // Hands-free listens and answers aloud, so it needs both directions.
  const canHandsFree = voice.dictation && voice.spokenReplies
  const activeVoiceMode = canHandsFree ? voiceMode : "cascade"

  const {
    isRecording,
    isTranscribing,
    recordingSeconds,
    volume,
    startRecording,
    stopRecording,
    cancelRecording,
    captureUtterance,
  } = useVoiceRecording()

  const { speakText, stopSpeaking } = useVoicePlayer()

  useEffect(() => {
    void fetchVoiceConfig()
      .then((config) => {
        setVoice(voiceCapabilities(config))
        setVoiceMode(config.mode || "cascade")
      })
      .catch(() => setVoice(NO_VOICE))
  }, [])

  useEffect(
    () => () => {
      handsFreeRef.current = false
      handsFreeGenerationRef.current += 1
      pendingHandsFreeReplyRef.current = false
      if (handsFreeReplyTimerRef.current !== null)
        window.clearTimeout(handsFreeReplyTimerRef.current)
    },
    [],
  )

  const handleToggleRecord = async () => {
    if (!canInput && !isRecording) return
    if (isRecording) {
      try {
        const cleaned = ((await stopRecording()) || "").trim()
        if (!cleaned || NON_SPEECH_TRANSCRIPT.test(cleaned)) {
          toast.info(t("chat.voice.noSpeech"))
          return
        }

        setInput(cleaned)
        const canReconnectChat =
          inputDisabledReason === "websocketConnecting" ||
          inputDisabledReason === "websocketDisconnected" ||
          inputDisabledReason === "websocketError"
        if (!canInput && !canReconnectChat) {
          toast.error(t("chat.voice.unavailable"))
          return
        }

        if (
          await sendVoiceMessage({
            content: cleaned,
            attachments: [],
            module: selectedModule,
            selection,
          })
        ) {
          setInput("")
        } else {
          toast.error(t("chat.voice.transcriptKept"))
        }
      } catch (err: unknown) {
        // Nothing was said: there is no message to send.
        if (err instanceof NoSpeechError) {
          toast.info(t("chat.voice.noSpeech"))
          return
        }
        toast.error(
          t("chat.voice.error", {
            error:
              err instanceof Error && err.message
                ? err.message
                : t("chat.voice.processFailed"),
          }),
        )
      }
    } else {
      try {
        await startRecording()
      } catch (err: unknown) {
        toast.error(
          err instanceof Error && err.message
            ? err.message
            : t("chat.voice.microphoneFailed"),
        )
      }
    }
  }

  const runHandsFreeTurn = async (generation: number) => {
    if (!handsFreeRef.current || generation !== handsFreeGenerationRef.current)
      return
    try {
      setLiveStatus("listening")
      let cleaned = ""
      try {
        cleaned = (await captureUtterance()).trim()
      } catch (cause) {
        // Silence or noise is not a turn: keep listening.
        if (!(cause instanceof NoSpeechError)) throw cause
      }
      if (
        !handsFreeRef.current ||
        generation !== handsFreeGenerationRef.current
      )
        return
      if (!cleaned || NON_SPEECH_TRANSCRIPT.test(cleaned)) {
        void runHandsFreeTurn(generation)
        return
      }
      setLiveStatus("transcribing")
      setInput(cleaned)
      pendingHandsFreeReplyRef.current = true
      pendingHandsFreeMessageCountRef.current = messages.length
      const sent = await sendVoiceMessage({
        content: cleaned,
        attachments: [],
        module: selectedModule,
        selection,
      })
      if (!sent) {
        pendingHandsFreeReplyRef.current = false
        toast.error(t("chat.voice.transcriptKept"))
        handsFreeRef.current = false
        setIsHandsFree(false)
        setLiveStatus("idle")
        return
      }
      setInput("")
      setLiveStatus("waiting")
      if (handsFreeReplyTimerRef.current !== null)
        window.clearTimeout(handsFreeReplyTimerRef.current)
      handsFreeReplyTimerRef.current = window.setTimeout(() => {
        if (!pendingHandsFreeReplyRef.current) return
        pendingHandsFreeReplyRef.current = false
        handsFreeRef.current = false
        setIsHandsFree(false)
        setLiveStatus("idle")
        toast.error(t("chat.voice.turnTimedOut"))
      }, 90_000)
    } catch (cause) {
      toast.error(
        cause instanceof Error && cause.message
          ? cause.message
          : t("chat.voice.handsFreeFailed"),
      )
      handsFreeRef.current = false
      setIsHandsFree(false)
      setLiveStatus("idle")
    }
  }
  const resumeHandsFree = useEffectEvent((generation: number) => {
    void runHandsFreeTurn(generation)
  })

  const handleToggleLive = () => {
    if (handsFreeRef.current) {
      handsFreeRef.current = false
      handsFreeGenerationRef.current += 1
      pendingHandsFreeReplyRef.current = false
      if (handsFreeReplyTimerRef.current !== null) {
        window.clearTimeout(handsFreeReplyTimerRef.current)
        handsFreeReplyTimerRef.current = null
      }
      cancelRecording()
      stopSpeaking()
      setIsHandsFree(false)
      setLiveStatus("idle")
      return
    }
    if (!canInput) {
      toast.error(t("chat.voice.unavailable"))
      return
    }
    handsFreeRef.current = true
    handsFreeGenerationRef.current += 1
    setIsHandsFree(true)
    void runHandsFreeTurn(handsFreeGenerationRef.current)
  }

  const lastMsg = messages[messages.length - 1]
  const lastSpokenIdRef = useRef<string | null>(null)
  const canAutoSpeak =
    isAutoSpeak &&
    voice.spokenReplies &&
    activeVoiceMode === "cascade" &&
    !isHandsFree

  useEffect(() => {
    if (!canAutoSpeak || !lastMsg) return
    if (lastMsg.role === "assistant" && !isTyping && lastMsg.content) {
      if (lastSpokenIdRef.current !== lastMsg.id) {
        lastSpokenIdRef.current = lastMsg.id
        void speakText(lastMsg.content).catch((cause) =>
          toast.error(
            cause instanceof Error && cause.message
              ? cause.message
              : t("chat.voice.playbackFailed"),
          ),
        )
      }
    }
  }, [canAutoSpeak, isTyping, lastMsg, speakText, t])

  useEffect(() => {
    if (
      !handsFreeRef.current ||
      !pendingHandsFreeReplyRef.current ||
      isTyping ||
      !lastMsg ||
      messages.length <= pendingHandsFreeMessageCountRef.current + 1 ||
      lastMsg.role !== "assistant" ||
      !lastMsg.content
    )
      return
    pendingHandsFreeReplyRef.current = false
    if (handsFreeReplyTimerRef.current !== null) {
      window.clearTimeout(handsFreeReplyTimerRef.current)
      handsFreeReplyTimerRef.current = null
    }
    const generation = handsFreeGenerationRef.current
    setLiveStatus("speaking")
    void speakText(lastMsg.content)
      .catch((cause) =>
        toast.error(
          cause instanceof Error && cause.message
            ? cause.message
            : t("chat.voice.playbackFailed"),
        ),
      )
      .finally(() => {
        if (
          handsFreeRef.current &&
          generation === handsFreeGenerationRef.current
        )
          resumeHandsFree(generation)
      })
  }, [isTyping, lastMsg, messages.length, speakText, t])

  const handleSend = () => {
    if ((!input.trim() && attachments.length === 0) || !canInput) return
    if (
      sendMessage({
        content: input,
        attachments,
        module: selectedModule,
        selection,
      })
    ) {
      setInput("")
      setAttachments([])
    }
  }

  const handleAddImages = () => {
    if (!canInput) return
    fileInputRef.current?.click()
  }

  const handleRemoveAttachment = (index: number) => {
    setAttachments((prev) => prev.filter((_, itemIndex) => itemIndex !== index))
  }

  const appendImageFiles = async (files: readonly File[]) => {
    if (!canInput || files.length === 0) {
      return
    }

    const nextAttachments = await buildChatImageAttachments(files, t)
    if (nextAttachments.length === 0) {
      return
    }

    setAttachments((prev) => [...prev, ...nextAttachments])
  }

  const handleImageSelection = async (event: ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(event.target.files ?? [])
    event.target.value = ""

    if (files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const resetDragState = () => {
    dragDepthRef.current = 0
    setIsDragActive(false)
  }

  const handleComposerPaste = async (
    event: ClipboardEvent<HTMLTextAreaElement>,
  ) => {
    const files = getTransferredFiles(event.clipboardData)
    if (files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const handleComposerDragEnter = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    if (!canInput) {
      return
    }
    dragDepthRef.current += 1
    setIsDragActive(true)
  }

  const handleComposerDragLeave = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    if (!canInput) {
      resetDragState()
      return
    }
    dragDepthRef.current = Math.max(0, dragDepthRef.current - 1)
    if (dragDepthRef.current === 0) {
      setIsDragActive(false)
    }
  }

  const handleComposerDragOver = (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    event.dataTransfer.dropEffect = canInput ? "copy" : "none"
  }

  const handleComposerDrop = async (event: DragEvent<HTMLDivElement>) => {
    if (!hasFileTransfer(event.dataTransfer)) {
      return
    }

    event.preventDefault()
    const files = getTransferredFiles(event.dataTransfer)
    resetDragState()

    if (!canInput || files.length === 0) {
      return
    }

    await appendImageFiles(files)
  }

  const canSubmit =
    canInput && (Boolean(input.trim()) || attachments.length > 0)

  return (
    <div className="bg-background/95 flex h-full flex-col">
      <PageHeader
        title={t("navigation.chat")}
        className={`transition-shadow ${
          hasScrolled ? "shadow-xs" : "shadow-none"
        }`}
        titleExtra={
          <div className="flex min-w-0 items-center gap-2">
            <ModelSelector
              selection={selection}
              defaultSelection={defaultSelection}
              targets={selectionTargets}
              routes={selectionRoutes}
              disabled={modelsLoading}
              onValueChange={setSelection}
            />
            {/* The connector gesture: selecting a module scopes the agent to
                it for the turn and loads that module's overlay and skills. */}
            <ModuleSelector
              value={selectedModule}
              onValueChange={(id) =>
                setSelectedModule(id === NO_MODULE ? "" : id)
              }
              onAvailableChange={setModuleCount}
            />
          </div>
        }
      >
        <DropdownMenu>
          <Tooltip delayDuration={500}>
            <TooltipTrigger asChild>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  className="text-muted-foreground hover:text-foreground size-9"
                  aria-label={t("chat.showAssistantDetails")}
                >
                  <IconAdjustmentsHorizontal className="size-4.5" />
                </Button>
              </DropdownMenuTrigger>
            </TooltipTrigger>
            <TooltipContent>{t("chat.showAssistantDetails")}</TooltipContent>
          </Tooltip>
          <DropdownMenuContent align="end" className="w-56">
            <DropdownMenuLabel>
              {t("chat.showAssistantDetails")}
            </DropdownMenuLabel>
            <DropdownMenuRadioGroup
              value={assistantDetailVisibility}
              onValueChange={(value) =>
                setAssistantDetailVisibility(value as AssistantDetailVisibility)
              }
            >
              {assistantDetailVisibilityOptions.map((option) => (
                <DropdownMenuRadioItem key={option.value} value={option.value}>
                  {option.label}
                </DropdownMenuRadioItem>
              ))}
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>

        <Button
          variant="secondary"
          size="sm"
          onClick={newChat}
          className="h-9 gap-2"
          aria-label={t("chat.newChat")}
        >
          <IconPlus className="size-4" />
          <span className="hidden sm:inline">{t("chat.newChat")}</span>
        </Button>

        <SessionHistoryMenu
          sessions={sessions}
          activeSessionId={activeSessionId}
          hasMore={hasMore}
          loadError={loadError}
          loadErrorMessage={loadErrorMessage}
          observerRef={observerRef}
          onOpenChange={(open) => {
            if (open) {
              void loadSessions(true)
            }
          }}
          onSwitchSession={switchSession}
          onDeleteSession={handleDeleteSession}
        />
      </PageHeader>

      <div
        ref={scrollRef}
        onScroll={handleScroll}
        className="min-h-0 flex-1 [scrollbar-gutter:stable] overflow-y-auto px-4 py-6 md:px-8 lg:px-24 xl:px-48"
      >
        <div className="mx-auto flex w-full max-w-250 flex-col gap-8 pb-8">
          {isHandsFree && (
            <div className="flex items-center justify-between gap-2 rounded-xl border border-emerald-500/30 bg-emerald-500/10 p-3 text-emerald-600 dark:text-emerald-400">
              <div role="status" className="flex min-w-0 items-center gap-2">
                <span className="size-2.5 shrink-0 animate-pulse rounded-full bg-emerald-500" />
                <span className="truncate text-xs font-medium">
                  {t("chat.voice.handsFreeStatus", {
                    status: t(`chat.voice.liveStatus.${liveStatus}`),
                  })}
                </span>
              </div>
              <Button
                size="sm"
                variant="ghost"
                className="h-6 shrink-0 text-xs text-red-500 hover:text-red-600"
                onClick={handleToggleLive}
              >
                {t("chat.voice.endCall")}
              </Button>
            </div>
          )}

          {messages.length === 0 && !isTyping && (
            <ChatEmptyState
              modelAvailability={modelAvailability}
              gatewayState={gwState}
              hasModules={moduleCount > 0}
            />
          )}

          {messages.map((msg) => {
            if (
              !shouldShowAssistantMessage(assistantDetailVisibility, msg.kind)
            ) {
              return null
            }

            return (
              <div key={msg.id} className="flex w-full min-w-0">
                {msg.role === "assistant" ? (
                  <AssistantMessage
                    content={msg.content}
                    attachments={msg.attachments}
                    kind={msg.kind}
                    modelName={
                      msg.modelName
                        ? formatModelLabel(
                            selectionLabel(msg.modelName, selectionTargets),
                          )
                        : undefined
                    }
                    modelTarget={msg.modelName}
                    toolCalls={msg.toolCalls}
                    timestamp={msg.timestamp}
                  />
                ) : (
                  <UserMessage
                    content={msg.content}
                    attachments={msg.attachments}
                    timestamp={msg.timestamp}
                  />
                )}
              </div>
            )
          })}

          {isTyping && <TypingIndicator />}
        </div>
      </div>

      <input
        ref={fileInputRef}
        type="file"
        accept={CHAT_IMAGE_ACCEPT}
        multiple
        className="hidden"
        onChange={handleImageSelection}
      />

      {modelLoadError && (
        <div className="border-destructive/30 bg-destructive/10 mx-4 mt-3 flex items-center justify-between gap-3 rounded-lg border px-3 py-2 text-sm md:mx-8">
          <span className="text-destructive">
            {t("chat.selection.loadFailed", { error: modelLoadError })}
          </span>
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={modelsLoading}
            onClick={retryModelLoad}
          >
            {t("common.retry")}
          </Button>
        </div>
      )}

      <ChatComposer
        input={input}
        attachments={attachments}
        onInputChange={setInput}
        onAddImages={handleAddImages}
        onPaste={handleComposerPaste}
        onDragEnter={handleComposerDragEnter}
        onDragLeave={handleComposerDragLeave}
        onDragOver={handleComposerDragOver}
        onDrop={handleComposerDrop}
        onRemoveAttachment={handleRemoveAttachment}
        onSend={handleSend}
        onContextDetail={() => {
          if (
            sendMessage({ content: "/context", attachments: [], selection })
          ) {
            setInput("")
          }
        }}
        inputDisabledReason={inputDisabledReason}
        canSend={canSubmit}
        isDragActive={isDragActive}
        contextUsage={contextUsage}
        canDictate={voice.dictation}
        canSpeak={voice.spokenReplies}
        voiceMode={activeVoiceMode}
        onVoiceModeChange={setVoiceMode}
        isRecording={isRecording}
        isTranscribing={isTranscribing}
        recordingSeconds={recordingSeconds}
        volume={volume}
        isLiveConnected={isHandsFree}
        liveStatus={liveStatus}
        onToggleRecord={handleToggleRecord}
        onCancelRecord={cancelRecording}
        onToggleLive={handleToggleLive}
        isAutoSpeak={isAutoSpeak}
        onToggleAutoSpeak={() => setIsAutoSpeak((prev) => !prev)}
        voiceDisabled={activeVoiceMode === "cascade" && !canInput}
      />
    </div>
  )
}
