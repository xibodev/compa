import dayjs from "dayjs"
import { useAtomValue } from "jotai"

import {
  newChatSession,
  sendChatMessage,
  sendChatMessageWhenReady,
  switchChatSession,
} from "@/features/chat/controller"
import { chatAtom, setChatSelection } from "@/store/chat"

// Live messages carry Unix-millisecond numbers; history carries RFC 3339 strings.
export function formatMessageTime(timestamp: number | string): string {
  const date = dayjs(timestamp)
  if (!date.isValid()) {
    return ""
  }
  const now = dayjs()

  const isToday = date.isSame(now, "day")
  const isThisYear = date.isSame(now, "year")

  if (isToday) {
    return date.format("LT")
  }

  if (isThisYear) {
    return date.format("MMM D LT")
  }

  return date.format("ll LT")
}

export function useWebChat() {
  const {
    messages,
    connectionState,
    isTyping,
    isTurnActive,
    activeSessionId,
    contextUsage,
    selectionBySession,
    failedDraft,
  } = useAtomValue(chatAtom)

  return {
    messages,
    connectionState,
    isTyping,
    isTurnActive,
    activeSessionId,
    contextUsage,
    failedDraft,
    selection: selectionBySession[activeSessionId] || "",
    setSelection: (selection: string) =>
      setChatSelection(activeSessionId, selection),
    sendMessage: sendChatMessage,
    sendVoiceMessage: sendChatMessageWhenReady,
    switchSession: switchChatSession,
    newChat: newChatSession,
  }
}
