import { getSessionHistory } from "@/api/sessions"
import {
  parseToolCallsValue,
  toolCallsSignature,
} from "@/features/chat/tool-calls"
import type { ChatAttachment, ChatMessage } from "@/store/chat"

export interface LoadedSession {
  messages: ChatMessage[]
  selection: string
}

function toChatAttachments({
  media,
  attachments,
}: {
  media?: string[]
  attachments?: {
    type?: "image" | "audio" | "video" | "file"
    url: string
    filename?: string
    content_type?: string
  }[]
}): ChatAttachment[] | undefined {
  const normalizedAttachments = attachments
    ?.filter((attachment) => attachment.url)
    .map(
      (attachment) =>
        ({
          type: attachment.type ?? "file",
          url: attachment.url,
          filename: attachment.filename,
          contentType: attachment.content_type,
        }) satisfies ChatAttachment,
    )

  const mediaAttachments = (media ?? [])
    .filter((item) => item.startsWith("data:image/"))
    .map((url) => ({ type: "image" as const, url }))

  const merged = [...(normalizedAttachments ?? []), ...mediaAttachments]

  return merged.length > 0 ? merged : undefined
}

export async function loadSessionMessages(
  sessionId: string,
): Promise<LoadedSession> {
  const detail = await getSessionHistory(sessionId)
  const messages = detail.messages.map((message, index) => ({
    id: `hist-${index}-${Date.now()}`,
    role: message.role,
    content: message.content,
    kind: message.role === "assistant" ? (message.kind ?? "normal") : undefined,
    modelName: message.model_name,
    servedTarget: message.served_target,
    servedIdentity: message.served_identity,
    toolCalls:
      message.role === "assistant"
        ? parseToolCallsValue(message.tool_calls)
        : undefined,
    attachments: toChatAttachments({
      media: message.media,
      attachments: message.attachments,
    }),
    timestamp: message.created_at ?? detail.updated,
  }))
  const selection = [...detail.messages]
    .reverse()
    .find((message) => message.role === "user")
    ?.requested_selection?.trim()
  return { messages, selection: selection ?? "" }
}

// Live messages carry Unix-millisecond numbers; history carries RFC 3339 strings.
function normalizeMessageTimestamp(timestamp: number | string): string {
  if (typeof timestamp === "number") {
    return String(timestamp)
  }

  const parsed = Date.parse(timestamp)
  return Number.isNaN(parsed) ? timestamp : String(parsed)
}

function messageSignature(message: ChatMessage): string {
  const attachmentSignature = (message.attachments ?? [])
    .map(
      (attachment) =>
        `${attachment.type}\u0001${attachment.url}\u0001${attachment.filename ?? ""}`,
    )
    .join("\u0002")

  return `${message.role}\u0000${message.content}\u0000${normalizeMessageTimestamp(
    message.timestamp,
  )}\u0000${message.kind ?? ""}\u0000${message.modelName ?? ""}\u0000${attachmentSignature}\u0000${toolCallsSignature(
    message.toolCalls,
  )}`
}

function comparableTimestamp(timestamp: number | string): number {
  const normalized = normalizeMessageTimestamp(timestamp)
  const numeric = Number(normalized)
  return Number.isFinite(numeric) ? numeric : 0
}

export function mergeHistoryMessages(
  historyMessages: ChatMessage[],
  currentMessages: ChatMessage[],
): ChatMessage[] {
  const currentIds = new Set(currentMessages.map((message) => message.id))
  const currentSignatures = new Set(
    currentMessages.map((message) => messageSignature(message)),
  )

  const merged = [
    ...historyMessages.filter(
      (message) =>
        !currentIds.has(message.id) &&
        !currentSignatures.has(messageSignature(message)),
    ),
    ...currentMessages,
  ]

  return merged.sort(
    (left, right) =>
      comparableTimestamp(left.timestamp) -
      comparableTimestamp(right.timestamp),
  )
}

// Live and saved copies of one message differ in id and timestamp, so after
// a reconnect they are matched by what they say.
function contentSignature(message: ChatMessage): string {
  const kind = message.role === "assistant" ? (message.kind ?? "normal") : ""
  return `${message.role}\u0000${kind}\u0000${message.content.trim()}`
}

/**
 * Merges the saved history, reloaded after a reconnect, with what the page
 * shows. The history is the record of what happened while the socket was
 * down, so it comes first; live messages it does not hold yet (a reply that
 * is still streaming, a message not saved yet) stay after it.
 */
export function mergeReconnectedHistory(
  historyMessages: ChatMessage[],
  currentMessages: ChatMessage[],
): ChatMessage[] {
  const saved = new Map<string, number>()
  for (const message of historyMessages) {
    const signature = contentSignature(message)
    saved.set(signature, (saved.get(signature) ?? 0) + 1)
  }

  const unsaved = currentMessages.filter((message) => {
    const signature = contentSignature(message)
    const count = saved.get(signature) ?? 0
    if (count === 0) {
      return true
    }
    saved.set(signature, count - 1)
    return false
  })

  return [...historyMessages, ...unsaved]
}
