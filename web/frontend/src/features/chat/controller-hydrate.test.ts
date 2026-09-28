import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const STORAGE_KEY = "compa:last-session-id"

const reply = (status: number, body: unknown = {}) =>
  Promise.resolve(new Response(JSON.stringify(body), { status }))

// The chat store reads the remembered session when its module loads, so
// each test loads the modules afresh after setting it.
async function loadChat(remembered: string) {
  localStorage.setItem(STORAGE_KEY, remembered)
  vi.resetModules()
  const controller = await import("./controller")
  const chat = await import("@/store/chat")
  return { ...controller, getChatState: chat.getChatState }
}

describe("restoring the last session", () => {
  beforeEach(() => {
    localStorage.clear()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it("starts fresh without a word when the session no longer exists", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {})
    vi.stubGlobal(
      "fetch",
      vi.fn(() => reply(404, { error: "session not found" })),
    )
    const { hydrateActiveSession, getChatState } = await loadChat("gone-1")
    expect(getChatState().activeSessionId).toBe("gone-1")

    await hydrateActiveSession()

    expect(error).not.toHaveBeenCalled()
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    const state = getChatState()
    expect(state.activeSessionId).not.toBe("gone-1")
    expect(state.activeSessionId).not.toBe("")
    expect(state.messages).toEqual([])
    expect(state.hasHydratedActiveSession).toBe(true)
  })

  it("still reports a history that fails to load", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {})
    vi.stubGlobal(
      "fetch",
      vi.fn(() => reply(500)),
    )
    const { hydrateActiveSession, getChatState } = await loadChat("kept-1")

    await hydrateActiveSession()

    expect(error).toHaveBeenCalledWith(
      "Failed to restore last session history:",
      expect.any(Error),
    )
    expect(getChatState().hasHydratedActiveSession).toBe(true)
  })

  it("restores the messages of a session that exists", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        reply(200, {
          id: "here-1",
          messages: [{ role: "user", content: "hello" }],
          summary: "",
          created: "2026-01-01T00:00:00Z",
          updated: "2026-01-01T00:00:00Z",
        }),
      ),
    )
    const { hydrateActiveSession, getChatState } = await loadChat("here-1")

    await hydrateActiveSession()

    const state = getChatState()
    expect(state.activeSessionId).toBe("here-1")
    expect(state.messages.map((message) => message.content)).toEqual(["hello"])
    expect(localStorage.getItem(STORAGE_KEY)).toBe("here-1")
  })
})
