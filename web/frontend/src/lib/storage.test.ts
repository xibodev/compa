import { afterEach, describe, expect, it, vi } from "vitest"

describe("guarded storage", () => {
  const original = Object.getOwnPropertyDescriptor(globalThis, "localStorage")

  afterEach(() => {
    if (original) Object.defineProperty(globalThis, "localStorage", original)
    else Reflect.deleteProperty(globalThis, "localStorage")
    vi.resetModules()
  })

  it("lets the chat store load when the browser blocks storage", async () => {
    // Reading localStorage itself throws in a browser that blocks storage.
    Object.defineProperty(globalThis, "localStorage", {
      configurable: true,
      get() {
        throw new DOMException("blocked", "SecurityError")
      },
    })
    vi.resetModules()

    const storage = await import("@/lib/storage")
    expect(storage.readStoredValue("anything")).toBeNull()
    expect(() => storage.writeStoredValue("anything", "x")).not.toThrow()
    expect(() => storage.removeStoredValue("anything")).not.toThrow()

    // The chat store reads the last session while its module loads.
    const chat = await import("@/store/chat")
    expect(chat.getChatState().activeSessionId).not.toBe("")
  })
})
