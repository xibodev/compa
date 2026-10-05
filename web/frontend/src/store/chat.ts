import { atom, getDefaultStore } from "jotai"
import { atomWithStorage } from "jotai/utils"

import {
  ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY,
  type AssistantDetailVisibility,
  DEFAULT_ASSISTANT_DETAIL_VISIBILITY,
  assistantDetailVisibilityStorage,
  shouldShowAssistantMessage,
} from "@/features/chat/detail-visibility"
import {
  getInitialActiveSessionId,
  writeStoredSessionId,
} from "@/features/chat/state"

export interface ChatAttachment {
  type: "image" | "audio" | "video" | "file"
  url: string
  filename?: string
  contentType?: string
}

export interface ChatToolCallFunction {
  name?: string
  arguments?: string
}

export interface ChatToolCallExtraContent {
  toolFeedbackExplanation?: string
}

export interface ChatToolCall {
  id?: string
  type?: string
  function?: ChatToolCallFunction
  extraContent?: ChatToolCallExtraContent
}

export type AssistantMessageKind = "normal" | "thought" | "tool_calls"

export interface ChatMessage {
  id: string
  role: "user" | "assistant"
  content: string
  timestamp: number | string
  kind?: AssistantMessageKind
  modelName?: string
  servedTarget?: string
  servedIdentity?: string
  attachments?: ChatAttachment[]
  toolCalls?: ChatToolCall[]
}

export function getChatSelection(sessionId: string): string {
  return getChatState().selectionBySession[sessionId] || ""
}

export function setChatSelection(sessionId: string, selection: string) {
  const normalized = selection.trim()
  updateChatStore((state) => {
    const selectionBySession = { ...state.selectionBySession }
    if (normalized) selectionBySession[sessionId] = normalized
    else delete selectionBySession[sessionId]
    return { selectionBySession }
  })
}

export interface ContextUsage {
  used_tokens: number
  total_tokens: number
  history_tokens?: number
  compress_at_tokens: number
  summarize_at_tokens?: number
  used_percent: number
}

export type ConnectionState =
  "disconnected" | "connecting" | "connected" | "error"

/** A message the server refused after the composer was cleared. */
export interface FailedDraft {
  content: string
  attachments?: ChatAttachment[]
}

export interface ChatStoreState {
  messages: ChatMessage[]
  connectionState: ConnectionState
  isTyping: boolean
  /**
   * A reply is being produced: from sending (or typing.start) until
   * typing.stop. isTyping ends at the first streamed chunk; this ends when
   * the reply is complete.
   */
  isTurnActive: boolean
  activeSessionId: string
  hasHydratedActiveSession: boolean
  contextUsage?: ContextUsage
  selectionBySession: Record<string, string>
  /** Text to put back in the composer, set until the chat page takes it. */
  failedDraft?: FailedDraft
}

type ChatStorePatch = Partial<ChatStoreState>

const DEFAULT_CHAT_STATE: ChatStoreState = {
  messages: [],
  connectionState: "disconnected",
  isTyping: false,
  isTurnActive: false,
  activeSessionId: getInitialActiveSessionId(),
  hasHydratedActiveSession: false,
  selectionBySession: {},
}

export const chatAtom = atom<ChatStoreState>(DEFAULT_CHAT_STATE)
export const assistantDetailVisibilityAtom =
  atomWithStorage<AssistantDetailVisibility>(
    ASSISTANT_DETAIL_VISIBILITY_STORAGE_KEY,
    DEFAULT_ASSISTANT_DETAIL_VISIBILITY,
    assistantDetailVisibilityStorage,
    { getOnInit: true },
  )

const store = getDefaultStore()

export function getChatState() {
  return store.get(chatAtom)
}

export function updateChatStore(
  patch:
    | ChatStorePatch
    | ((prev: ChatStoreState) => ChatStorePatch | ChatStoreState),
) {
  store.set(chatAtom, (prev) => {
    const nextPatch = typeof patch === "function" ? patch(prev) : patch
    const next = { ...prev, ...nextPatch }

    if (next.activeSessionId !== prev.activeSessionId) {
      writeStoredSessionId(next.activeSessionId)
    }

    return next
  })
}

export function clearFailedDraft() {
  updateChatStore({ failedDraft: undefined })
}

export { shouldShowAssistantMessage }
export type { AssistantDetailVisibility }
