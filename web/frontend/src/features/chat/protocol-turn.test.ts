import { beforeEach, describe, expect, it, vi } from "vitest"

import { getChatState, updateChatStore } from "@/store/chat"

import { mergeReconnectedHistory } from "./history"
import { handleWebChatMessage } from "./protocol"

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn() },
}))

const SESSION = "s-1"

describe("web chat frames", () => {
  beforeEach(() => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    // No turn is open: a test that starts one ends it here at the latest.
    handleWebChatMessage({ type: "turn.end" }, SESSION)
    updateChatStore({
      messages: [],
      isTyping: false,
      isTurnActive: false,
      failedDraft: undefined,
    })
  })

  it("keeps the turn running past the first streamed chunk until typing stops", () => {
    handleWebChatMessage({ type: "typing.start" }, SESSION)
    handleWebChatMessage(
      {
        type: "message.create",
        payload: { message_id: "a1", content: "Hel" },
      },
      SESSION,
    )
    // The indicator gives way to the streaming text, but the reply is not done.
    expect(getChatState().isTyping).toBe(false)
    expect(getChatState().isTurnActive).toBe(true)

    handleWebChatMessage(
      {
        type: "message.update",
        payload: { message_id: "a1", content: "Hello" },
      },
      SESSION,
    )
    handleWebChatMessage({ type: "typing.stop" }, SESSION)

    expect(getChatState().isTurnActive).toBe(false)
    expect(getChatState().messages.map((m) => m.content)).toEqual(["Hello"])
  })

  it("keeps a turn running when typing stops mid-turn, until turn.end", () => {
    handleWebChatMessage({ type: "typing.start" }, SESSION)
    handleWebChatMessage(
      { type: "turn.start", payload: { request_id: "msg-1" } },
      SESSION,
    )
    // A message sent mid-turn stops the typing of the one before it.
    handleWebChatMessage({ type: "typing.stop" }, SESSION)
    expect(getChatState().isTyping).toBe(false)
    expect(getChatState().isTurnActive).toBe(true)

    handleWebChatMessage(
      {
        type: "message.create",
        payload: { message_id: "a1", content: "Done" },
      },
      SESSION,
    )
    expect(getChatState().isTurnActive).toBe(true)

    handleWebChatMessage(
      {
        type: "turn.end",
        payload: { request_id: "msg-1", status: "completed" },
      },
      SESSION,
    )
    expect(getChatState().isTurnActive).toBe(false)
  })

  it("gives a refused message back to the composer", () => {
    const attachments = [{ type: "image" as const, url: "data:image/png;x" }]
    updateChatStore({
      messages: [
        {
          id: "msg-1",
          role: "user",
          content: "look at this",
          attachments,
          timestamp: 1,
        },
      ],
      isTyping: true,
      isTurnActive: true,
    })

    handleWebChatMessage(
      {
        type: "error",
        payload: {
          code: "invalid_media",
          message: "bad image",
          request_id: "msg-1",
        },
      },
      SESSION,
    )

    const state = getChatState()
    expect(state.messages).toEqual([])
    expect(state.isTurnActive).toBe(false)
    expect(state.failedDraft).toEqual({ content: "look at this", attachments })
  })

  it("leaves the composer alone for an error about no sent message", () => {
    handleWebChatMessage(
      { type: "error", payload: { message: "busy" } },
      SESSION,
    )
    expect(getChatState().failedDraft).toBeUndefined()
  })
})

describe("mergeReconnectedHistory", () => {
  const msg = (
    id: string,
    role: "user" | "assistant",
    content: string,
    timestamp: number | string = 0,
  ) => ({
    id,
    role,
    content,
    timestamp,
    ...(role === "assistant" ? { kind: "normal" as const } : {}),
  })

  it("adds what was missed while disconnected without doubling what was seen", () => {
    const shown = [
      msg("msg-1", "user", "hi", 1000),
      msg("a1", "assistant", "hello", 1001),
    ]
    const saved = [
      msg("hist-0", "user", "hi", "2026-01-01T00:00:00Z"),
      msg("hist-1", "assistant", "hello", "2026-01-01T00:00:01Z"),
      msg("hist-2", "user", "and then?", "2026-01-01T00:00:02Z"),
      msg("hist-3", "assistant", "then this", "2026-01-01T00:00:03Z"),
    ]

    expect(mergeReconnectedHistory(saved, shown).map((m) => m.content)).toEqual(
      ["hi", "hello", "and then?", "then this"],
    )
  })

  it("keeps a live message the history does not hold yet", () => {
    const shown = [
      msg("msg-1", "user", "hi"),
      msg("a1", "assistant", "partial"),
    ]
    const saved = [msg("hist-0", "user", "hi")]

    expect(mergeReconnectedHistory(saved, shown).map((m) => m.id)).toEqual([
      "hist-0",
      "a1",
    ])
  })
})
