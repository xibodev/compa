import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

const STORAGE_KEY = "compa:last-session-id"

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn() },
}))

const reply = (status: number, body: unknown = {}) =>
  Promise.resolve(new Response(JSON.stringify(body), { status }))

// The chat store reads the remembered session when its module loads, so
// each test loads the modules afresh after setting it.
async function loadChat(remembered: string) {
  localStorage.setItem(STORAGE_KEY, remembered)
  vi.resetModules()
  const controller = await import("./controller")
  const chat = await import("@/store/chat")
  const { toast } = await import("sonner")
  return { ...controller, getChatState: chat.getChatState, toast }
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

  it("keeps a session whose history fails to load, says so, and does not continue it unseen", async () => {
    const error = vi.spyOn(console, "error").mockImplementation(() => {})
    vi.stubGlobal(
      "fetch",
      vi.fn(() => reply(500)),
    )
    const { hydrateActiveSession, getChatState, toast } =
      await loadChat("kept-1")

    await hydrateActiveSession()

    expect(error).toHaveBeenCalledWith(
      "Failed to restore last session history:",
      expect.any(Error),
    )
    expect(toast.error).toHaveBeenCalledWith("Failed to load chat history")
    const state = getChatState()
    // Still the same conversation, remembered for the next load...
    expect(state.activeSessionId).toBe("kept-1")
    expect(localStorage.getItem(STORAGE_KEY)).toBe("kept-1")
    // ...but not hydrated, which keeps the socket from connecting to it.
    expect(state.hasHydratedActiveSession).toBe(false)
  })

  it("lets a new chat leave a session whose history failed to load", async () => {
    vi.spyOn(console, "error").mockImplementation(() => {})
    vi.stubGlobal(
      "fetch",
      vi.fn(() => reply(500)),
    )
    const { hydrateActiveSession, newChatSession, getChatState } =
      await loadChat("kept-2")
    await hydrateActiveSession()

    await newChatSession()

    const state = getChatState()
    expect(state.activeSessionId).not.toBe("kept-2")
    expect(state.hasHydratedActiveSession).toBe(true)
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
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

describe("switching sessions", () => {
  beforeEach(() => {
    localStorage.clear()
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it("keeps the last chosen session when an earlier load finishes later", async () => {
    const pending = new Map<string, (response: Response) => void>()
    vi.stubGlobal(
      "fetch",
      vi.fn(
        (input: RequestInfo | URL) =>
          new Promise<Response>((resolve) => {
            const id = decodeURIComponent(String(input).split("/").pop() ?? "")
            pending.set(id, resolve)
          }),
      ),
    )
    const history = (id: string) =>
      new Response(
        JSON.stringify({
          id,
          messages: [{ role: "user", content: `in ${id}` }],
          summary: "",
          created: "2026-01-01T00:00:00Z",
          updated: "2026-01-01T00:00:00Z",
        }),
        { status: 200 },
      )
    localStorage.clear()
    vi.resetModules()
    const { switchChatSession } = await import("./controller")
    const { getChatState } = await import("@/store/chat")

    const slow = switchChatSession("slow")
    const fast = switchChatSession("fast")
    await vi.waitFor(() => expect(pending.size).toBe(2))

    pending.get("fast")!(history("fast"))
    await fast
    pending.get("slow")!(history("slow"))
    await slow

    const state = getChatState()
    expect(state.activeSessionId).toBe("fast")
    expect(state.messages.map((message) => message.content)).toEqual([
      "in fast",
    ])
  })
})
