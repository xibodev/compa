import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn() },
}))

class FakeSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  static instances: FakeSocket[] = []

  url: string
  readyState = FakeSocket.CONNECTING
  sent: string[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null

  constructor(url: string) {
    this.url = url
    FakeSocket.instances.push(this)
  }

  send(data: string) {
    this.sent.push(data)
  }

  close() {
    this.readyState = FakeSocket.CLOSED
  }

  open() {
    this.readyState = FakeSocket.OPEN
    this.onopen?.()
  }

  receive(frame: unknown) {
    this.onmessage?.({ data: JSON.stringify(frame) })
  }
}

describe("the chat connection", () => {
  beforeEach(() => {
    vi.useFakeTimers()
    localStorage.clear()
    FakeSocket.instances = []
    vi.stubGlobal("WebSocket", FakeSocket)
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.restoreAllMocks()
  })

  it("drops a connection that stops answering pings and reloads the history after reconnecting", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {})
    const fetchMock = vi.fn(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            id: "s",
            messages: [{ role: "assistant", content: "missed reply" }],
            summary: "",
            created: "2026-01-01T00:00:00Z",
            updated: "2026-01-01T00:00:00Z",
          }),
          { status: 200 },
        ),
      ),
    )
    vi.stubGlobal("fetch", fetchMock)
    vi.resetModules()
    const controller = await import("./controller")
    const { updateGatewayStore } = await import("@/store/gateway")
    const { getChatState } = await import("@/store/chat")
    updateGatewayStore({ status: "running" })

    controller.initializeChatStore()
    const first = FakeSocket.instances[0]
    first.open()
    expect(getChatState().connectionState).toBe("connected")
    expect(fetchMock).not.toHaveBeenCalled()

    vi.advanceTimersByTime(controller.PING_INTERVAL_MS)
    expect(JSON.parse(first.sent[0])).toMatchObject({ type: "ping" })

    // A pong keeps the connection.
    first.receive({ type: "pong" })
    vi.advanceTimersByTime(controller.PONG_TIMEOUT_MS)
    expect(getChatState().connectionState).toBe("connected")

    // Silence after the next ping drops it, even though it still looks open.
    vi.advanceTimersByTime(
      controller.PING_INTERVAL_MS - controller.PONG_TIMEOUT_MS,
    )
    expect(first.sent).toHaveLength(2)
    vi.advanceTimersByTime(controller.PONG_TIMEOUT_MS)
    expect(getChatState().connectionState).toBe("disconnected")
    expect(first.readyState).toBe(FakeSocket.CLOSED)

    // It reconnects, and fetches what it missed meanwhile.
    vi.advanceTimersByTime(1000)
    const second = FakeSocket.instances[1]
    expect(second).toBeDefined()
    second.open()

    await vi.waitFor(() =>
      expect(getChatState().messages.map((m) => m.content)).toEqual([
        "missed reply",
      ]),
    )
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })
})
