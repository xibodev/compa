import { toast } from "sonner"

import {
  parseAssistantMessageCreateState,
  parseAssistantMessageUpdateState,
} from "@/features/chat/assistant-message-state"
import {
  type ChatAttachment,
  type ContextUsage,
  updateChatStore,
} from "@/store/chat"

export interface WebChatMessage {
  type: string
  id?: string
  session_id?: string
  /** Server send time in Unix milliseconds. */
  timestamp?: number
  payload?: Record<string, unknown>
}

/**
 * The session whose turn started (turn.start) and hasn't ended (turn.end).
 */
let openTurnSession = ""

function parseAttachments(
  payload: Record<string, unknown>,
): ChatAttachment[] | undefined {
  const raw = payload.attachments
  if (!Array.isArray(raw)) {
    return undefined
  }

  const attachments: ChatAttachment[] = []
  for (const item of raw) {
    if (!item || typeof item !== "object") {
      continue
    }

    const attachment = item as Record<string, unknown>
    const url = typeof attachment.url === "string" ? attachment.url : ""
    if (!url) {
      continue
    }

    const type =
      attachment.type === "audio" ||
      attachment.type === "video" ||
      attachment.type === "file" ||
      attachment.type === "image"
        ? attachment.type
        : "file"

    const filename =
      typeof attachment.filename === "string" ? attachment.filename : undefined
    const contentType =
      typeof attachment.content_type === "string"
        ? attachment.content_type
        : undefined

    attachments.push({
      type,
      url,
      ...(filename ? { filename } : {}),
      ...(contentType ? { contentType } : {}),
    })
  }

  return attachments.length > 0 ? attachments : undefined
}

function parseContextUsage(
  payload: Record<string, unknown>,
): ContextUsage | undefined {
  const raw = payload.context_usage
  if (!raw || typeof raw !== "object") return undefined
  const obj = raw as Record<string, unknown>
  const used = Number(obj.used_tokens)
  const total = Number(obj.total_tokens)
  if (!Number.isFinite(used) || !Number.isFinite(total) || total <= 0)
    return undefined
  return {
    used_tokens: used,
    total_tokens: total,
    history_tokens:
      obj.history_tokens != null ? Number(obj.history_tokens) : undefined,
    compress_at_tokens: Number(obj.compress_at_tokens) || 0,
    summarize_at_tokens:
      obj.summarize_at_tokens != null
        ? Number(obj.summarize_at_tokens)
        : undefined,
    used_percent: Number(obj.used_percent) || 0,
  }
}

function parseModelName(payload: Record<string, unknown>): string | undefined {
  if (typeof payload.model_name !== "string") {
    return undefined
  }
  const modelName = payload.model_name.trim()
  return modelName || undefined
}

function parseString(payload: Record<string, unknown>, key: string) {
  const value = payload[key]
  return typeof value === "string" && value.trim() ? value.trim() : undefined
}

export function handleWebChatMessage(
  message: WebChatMessage,
  expectedSessionId: string,
) {
  if (message.session_id && message.session_id !== expectedSessionId) {
    return
  }

  const payload = message.payload || {}

  switch (message.type) {
    case "message.create":
    case "media.create": {
      const messageId = (payload.message_id as string) || `web-${Date.now()}`
      const { content, kind, toolCalls } =
        parseAssistantMessageCreateState(payload)
      const attachments = parseAttachments(payload)
      const contextUsage = parseContextUsage(payload)
      const isPlaceholder = payload.placeholder === true
      const modelName = parseModelName(payload)
      const servedTarget = parseString(payload, "served_target")
      const servedIdentity = parseString(payload, "served_identity")
      const timestamp = message.timestamp ?? Date.now()

      updateChatStore((prev) => ({
        messages: [
          ...prev.messages,
          {
            id: messageId,
            role: "assistant",
            content,
            kind,
            ...(servedTarget || modelName
              ? { modelName: servedTarget || modelName }
              : {}),
            ...(servedTarget ? { servedTarget } : {}),
            ...(servedIdentity ? { servedIdentity } : {}),
            ...(toolCalls ? { toolCalls } : {}),
            attachments,
            timestamp,
          },
        ],
        isTyping:
          !isPlaceholder &&
          (kind === "normal" || message.type === "media.create")
            ? false
            : prev.isTyping,
        ...(contextUsage ? { contextUsage } : {}),
      }))
      break
    }

    case "message.update": {
      const messageId = payload.message_id as string
      const attachments = parseAttachments(payload)
      const contextUsage = parseContextUsage(payload)
      const modelName = parseModelName(payload)
      const servedTarget = parseString(payload, "served_target")
      const servedIdentity = parseString(payload, "served_identity")
      const timestamp = message.timestamp ?? Date.now()
      if (!messageId) {
        break
      }

      updateChatStore((prev) => ({
        messages: (() => {
          let found = false
          const messages = prev.messages.map((msg) => {
            if (msg.id !== messageId) {
              return msg
            }
            found = true
            const { content, kind, toolCalls } =
              parseAssistantMessageUpdateState(payload, msg)
            return {
              ...msg,
              id: messageId,
              content,
              kind,
              toolCalls,
              ...(servedTarget || modelName
                ? { modelName: servedTarget || modelName }
                : {}),
              ...(servedTarget ? { servedTarget } : {}),
              ...(servedIdentity ? { servedIdentity } : {}),
              ...(attachments ? { attachments } : {}),
            }
          })
          if (found) {
            return messages
          }

          const { content, kind, toolCalls } =
            parseAssistantMessageUpdateState(payload)

          return [
            ...messages,
            {
              id: messageId,
              role: "assistant" as const,
              content,
              kind,
              toolCalls,
              ...(servedTarget || modelName
                ? { modelName: servedTarget || modelName }
                : {}),
              ...(servedTarget ? { servedTarget } : {}),
              ...(servedIdentity ? { servedIdentity } : {}),
              ...(attachments ? { attachments } : {}),
              timestamp,
            },
          ]
        })(),
        ...(contextUsage ? { contextUsage } : {}),
      }))
      break
    }

    case "message.delete": {
      const messageId = payload.message_id as string
      if (!messageId) {
        break
      }

      updateChatStore((prev) => ({
        messages: prev.messages.filter((msg) => msg.id !== messageId),
      }))
      break
    }

    case "typing.start":
      updateChatStore({ isTyping: true, isTurnActive: true })
      break

    case "typing.stop":
      // Inside a turn, typing can stop before the reply is done, as when a
      // message arrives mid-turn: turn.end ends the turn. Without turn
      // frames, as for a command, typing.stop does.
      updateChatStore(
        openTurnSession === expectedSessionId
          ? { isTyping: false }
          : { isTyping: false, isTurnActive: false },
      )
      break

    case "turn.start":
      openTurnSession = expectedSessionId
      updateChatStore({ isTurnActive: true })
      break

    case "turn.end":
      openTurnSession = ""
      updateChatStore({ isTyping: false, isTurnActive: false })
      break

    case "error": {
      const requestId =
        typeof payload.request_id === "string" ? payload.request_id : ""
      const errorMessage =
        typeof payload.message === "string" ? payload.message : ""

      console.error("Web chat error:", payload)
      if (errorMessage) {
        toast.error(errorMessage)
      }
      updateChatStore((prev) => {
        // The refused message leaves the transcript, and its text goes back
        // to the composer: the composer was cleared when it was sent.
        const refused = requestId
          ? prev.messages.find(
              (msg) => msg.id === requestId && msg.role === "user",
            )
          : undefined
        return {
          messages: requestId
            ? prev.messages.filter((msg) => msg.id !== requestId)
            : prev.messages,
          isTyping: false,
          isTurnActive: false,
          ...(refused
            ? {
                failedDraft: {
                  content: refused.content,
                  ...(refused.attachments?.length
                    ? { attachments: refused.attachments }
                    : {}),
                },
              }
            : {}),
        }
      })
      break
    }

    case "pong":
      break

    default:
      console.log("Unknown web chat message type:", message.type)
  }
}
