import { IconAdjustmentsHorizontal, IconPlus } from "@tabler/icons-react"
import { CatchBoundary } from "@tanstack/react-router"
import { useAtom } from "jotai"
import {
  type ChangeEvent,
  type ClipboardEvent,
  type DragEvent,
  memo,
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
import { finishedReply } from "@/features/chat/finished-reply"
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
import { speakableText } from "@/features/voice/speakable-text"
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
import type { ChatAttachment, ChatMessage } from "@/store/chat"
import {
  assistantDetailVisibilityAtom,
  clearFailedDraft,
  shouldShowAssistantMessage,
} from "@/store/chat"
import type { GatewayState } from "@/store/gateway"

const NO_VOICE: VoiceCapabilities = { dictation: false, spokenReplies: false }

// Transcripts some models return for audio without speech.
const NON_SPEECH_TRANSCRIPT = /^[[(].*[\])]$/

// How long a finished reply must stay unchanged before it is spoken: the
// server may stop typing just before the last chunk or placeholder edit.
const REPLY_SETTLE_MS = 400

/**
 * One message of the transcript. Memoized so a streamed chunk re-renders
 * only the message it changes, and fenced so a message that cannot render
 * breaks only itself, not the page with its New Chat and History buttons.
 */
const ChatMessageItem = memo(function ChatMessageItem({
  message,
  modelName,
}: {
  message: ChatMessage
  modelName?: string
}) {
  return (
    <div className="flex w-full min-w-0">
      <CatchBoundary
        getResetKey={() => message.content}
        onCatch={(error) =>
          console.error("Failed to render a chat message:", error)
        }
      >
        {message.role === "assistant" ? (
          <AssistantMessage
            content={message.content}
            attachments={message.attachments}
            kind={message.kind}
            modelName={modelName}
            modelTarget={message.modelName}
            toolCalls={message.toolCalls}
            timestamp={message.timestamp}
          />
        ) : (
          <UserMessage
            content={message.content}
            attachments={message.attachments}
            timestamp={message.timestamp}
          />
        )}
      </CatchBoundary>
    </div>
  )
})

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
    isTurnActive,
    activeSessionId,
    contextUsage,
    failedDraft,
    sendMessage,
    sendVoiceMessage,
    selection,
    setSelection,
    switchSession,
    newChat,
  } = useWebChat()

  // A message the server refused comes back to the composer instead of being
  // lost; text typed since stays after it.
  useEffect(() => {
    if (!failedDraft) return
    setInput((current) =>
      current.trim()
        ? `${failedDraft.content}\n${current}`
        : failedDraft.content,
    )
    const restoredAttachments = failedDraft.attachments ?? []
    if (restoredAttachments.length > 0) {
      setAttachments((current) => [...restoredAttachments, ...current])
    }
    clearFailedDraft()
  }, [failedDraft])

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
  const handsFreeReplyTimerRef = useRef<number | null>(null)
  // A turn ran since the last reply was spoken, so the next finished reply
  // answers it. Replies loaded from history are never spoken.
  const replyExpectedRef = useRef(false)
  // What a hands-free turn sends with. A turn outlives the render it started
  // in, so it reads the current model and module here rather than the ones
  // it closed over.
  const turnContextRef = useRef({ selection, selectedModule })
  useEffect(() => {
    turnContextRef.current = { selection, selectedModule }
  }, [selection, selectedModule])
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

  useEffect(() => {
    if (isTurnActive) replyExpectedRef.current = true
  }, [isTurnActive])

  // Another conversation's last answer is history, not a reply to speak.
  useEffect(() => {
    replyExpectedRef.current = false
  }, [activeSessionId])

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
        console.error("Failed to start the microphone:", err)
        toast.error(t("chat.voice.microphoneFailed"))
      }
    }
  }

  const stopHandsFree = () => {
    handsFreeRef.current = false
    setIsHandsFree(false)
    setLiveStatus("idle")
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
      const { selection: turnSelection, selectedModule: turnModule } =
        turnContextRef.current
      const sent = await sendVoiceMessage({
        content: cleaned,
        attachments: [],
        module: turnModule,
        selection: turnSelection,
      })
      if (!sent) {
        pendingHandsFreeReplyRef.current = false
        toast.error(t("chat.voice.transcriptKept"))
        stopHandsFree()
        return
      }
      setInput("")
      setLiveStatus("waiting")
      if (handsFreeReplyTimerRef.current !== null)
        window.clearTimeout(handsFreeReplyTimerRef.current)
      handsFreeReplyTimerRef.current = window.setTimeout(() => {
        if (!pendingHandsFreeReplyRef.current) return
        pendingHandsFreeReplyRef.current = false
        stopHandsFree()
        toast.error(t("chat.voice.turnTimedOut"))
      }, 90_000)
    } catch (cause) {
      console.error("Hands-free voice failed:", cause)
      toast.error(t("chat.voice.handsFreeFailed"))
      stopHandsFree()
    }
  }

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
      stopHandsFree()
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

  const canAutoSpeak =
    isAutoSpeak &&
    voice.spokenReplies &&
    activeVoiceMode === "cascade" &&
    !isHandsFree

  const playbackFailed = (cause: unknown) => {
    console.error("Failed to play the reply:", cause)
    toast.error(t("chat.voice.playbackFailed"))
  }

  // Speaks a reply once it is complete: a streamed reply grows until its turn
  // ends, so speaking earlier would read only its first chunk. Hands-free
  // listens again only after the reply has been spoken.
  const onReplyFinished = useEffectEvent((content: string) => {
    const text = speakableText(content)
    if (handsFreeRef.current && pendingHandsFreeReplyRef.current) {
      pendingHandsFreeReplyRef.current = false
      replyExpectedRef.current = false
      if (handsFreeReplyTimerRef.current !== null) {
        window.clearTimeout(handsFreeReplyTimerRef.current)
        handsFreeReplyTimerRef.current = null
      }
      const generation = handsFreeGenerationRef.current
      setLiveStatus("speaking")
      void speakText(text)
        .catch(playbackFailed)
        .finally(() => {
          if (
            handsFreeRef.current &&
            generation === handsFreeGenerationRef.current
          )
            void runHandsFreeTurn(generation)
        })
      return
    }
    if (!replyExpectedRef.current) return
    replyExpectedRef.current = false
    if (canAutoSpeak && text) void speakText(text).catch(playbackFailed)
  })

  const reply = finishedReply(messages, isTurnActive)
  const replyId = reply?.id ?? ""
  const replyContent = reply?.content ?? ""
  useEffect(() => {
    if (!replyId) return
    const timer = window.setTimeout(
      () => onReplyFinished(replyContent),
      REPLY_SETTLE_MS,
    )
    return () => window.clearTimeout(timer)
  }, [replyId, replyContent])

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
        <div
          role="log"
          aria-live="polite"
          aria-busy={isTurnActive}
          className="mx-auto flex w-full max-w-250 flex-col gap-8 pb-8"
        >
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

          {messages.map((msg) =>
            shouldShowAssistantMessage(assistantDetailVisibility, msg.kind) ? (
              <ChatMessageItem
                key={msg.id}
                message={msg}
                modelName={
                  msg.role === "assistant" && msg.modelName
                    ? formatModelLabel(
                        selectionLabel(msg.modelName, selectionTargets),
                      )
                    : undefined
                }
              />
            ) : null,
          )}

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
