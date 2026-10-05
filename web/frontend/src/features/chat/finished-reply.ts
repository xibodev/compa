import type { ChatMessage } from "@/store/chat"

/**
 * The reply a turn ended with: the assistant's normal messages after the last
 * user message, joined, once no turn is running. Null while a turn runs,
 * because a streamed reply is still growing and only its end is the reply.
 */
export function finishedReply(
  messages: ChatMessage[],
  isTurnActive: boolean,
): { id: string; content: string } | null {
  if (isTurnActive) return null
  const parts: string[] = []
  let lastId = ""
  for (let index = messages.length - 1; index >= 0; index -= 1) {
    const message = messages[index]
    if (message.role === "user") break
    if ((message.kind ?? "normal") !== "normal") continue
    if (!lastId) lastId = message.id
    parts.unshift(message.content)
  }
  const content = parts.join("\n\n").trim()
  return lastId && content ? { id: lastId, content } : null
}
