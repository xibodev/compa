import { describe, expect, it } from "vitest"

import type { ChatMessage } from "@/store/chat"

import { finishedReply } from "./finished-reply"

const user = (id: string, content: string): ChatMessage => ({
  id,
  role: "user",
  content,
  timestamp: 0,
})
const assistant = (
  id: string,
  content: string,
  kind: ChatMessage["kind"] = "normal",
): ChatMessage => ({ id, role: "assistant", content, kind, timestamp: 0 })

describe("finishedReply", () => {
  it("has no reply while the turn still runs, so a streamed first chunk is not taken for the reply", () => {
    const messages = [user("u1", "hi"), assistant("a1", "Hel")]
    expect(finishedReply(messages, true)).toBeNull()
    expect(finishedReply(messages, false)).toEqual({
      id: "a1",
      content: "Hel",
    })
  })

  it("joins every answer after the last user message and skips thoughts and tool calls", () => {
    const messages = [
      assistant("old", "an earlier answer"),
      user("u1", "question"),
      assistant("t1", "thinking", "thought"),
      assistant("a1", "first part"),
      assistant("c1", "", "tool_calls"),
      assistant("a2", "second part"),
    ]

    expect(finishedReply(messages, false)).toEqual({
      id: "a2",
      content: "first part\n\nsecond part",
    })
  })

  it("has no reply when the last message is the user's", () => {
    expect(
      finishedReply([assistant("a1", "x"), user("u1", "y")], false),
    ).toBeNull()
  })
})
