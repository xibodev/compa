import { act, renderHook, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import { useDefaultModel } from "./use-default-model"

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    warning: vi.fn(),
    info: vi.fn(),
  },
}))

const selectable = new Set(["first/model", "primary"])
let stored = ""
let restartRequired = false

const reply = (body: unknown, status = 200) =>
  Promise.resolve(
    typeof body === "string"
      ? new Response(body, { status })
      : new Response(JSON.stringify(body), { status }),
  )

function fakeServer(input: RequestInfo | URL, init?: RequestInit) {
  const path = String(input)
  if (path === "/api/gateway/status")
    return reply({
      gateway_status: "running",
      gateway_restart_required: restartRequired,
    })
  if (path !== "/api/default-model") return reply("missing", 404)
  if (init?.method === "PUT") {
    const { selection } = JSON.parse(String(init.body))
    if (selection && !selectable.has(selection))
      return reply(`selection "${selection}" does not resolve\n`, 400)
    stored = selection
  }
  return reply({ selection: stored })
}

describe("useDefaultModel", () => {
  beforeEach(() => {
    stored = "first/model"
    restartRequired = false
    vi.stubGlobal("fetch", vi.fn(fakeServer))
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.clearAllMocks()
  })

  const loadedHook = async () => {
    const hook = renderHook(() => useDefaultModel())
    await waitFor(() => expect(hook.result.current.loaded).toBe(true))
    return hook
  }

  it("loads the configured default selection", async () => {
    const { result } = renderHook(() => useDefaultModel())
    expect(result.current).toMatchObject({ loaded: false, loading: true })
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current).toMatchObject({
      selection: "first/model",
      loaded: true,
      error: "",
    })
  })

  it("tells no default apart from an unknown default", async () => {
    stored = ""
    const { result } = await loadedHook()
    expect(result.current.selection).toBe("")
  })

  it("sets a route as the default and confirms it", async () => {
    const { result } = await loadedHook()
    let accepted = false
    await act(async () => {
      accepted = await result.current.setDefault("primary")
    })
    expect(accepted).toBe(true)
    expect(result.current.selection).toBe("primary")
    expect(result.current.saving).toBe(false)
    expect(stored).toBe("primary")
    expect(toast.success).toHaveBeenCalledWith("Default model updated.")
  })

  it("clears the default with an empty selection", async () => {
    const { result } = await loadedHook()
    await act(async () => {
      await result.current.setDefault("")
    })
    expect(result.current.selection).toBe("")
    expect(stored).toBe("")
    expect(toast.success).toHaveBeenCalledWith("Default model cleared.")
  })

  it("warns when the gateway needs a restart to use the new default", async () => {
    restartRequired = true
    const { result } = await loadedHook()
    await act(async () => {
      await result.current.setDefault("primary")
    })
    expect(toast.warning).toHaveBeenCalledWith(
      "Gateway restart required",
      expect.objectContaining({
        description: expect.stringContaining("default model"),
      }),
    )
    expect(toast.success).not.toHaveBeenCalled()
  })

  it("keeps the current default when the server rejects a selection", async () => {
    const { result } = await loadedHook()
    let accepted = true
    await act(async () => {
      accepted = await result.current.setDefault("gone/model")
    })
    expect(accepted).toBe(false)
    expect(result.current.selection).toBe("first/model")
    expect(result.current.saving).toBe(false)
    expect(toast.error).toHaveBeenCalledWith(
      'selection "gone/model" does not resolve',
    )
  })

  it("reports a failed load without claiming there is no default", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => reply("config unreadable", 500)),
    )
    const { result } = renderHook(() => useDefaultModel())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current).toMatchObject({
      loaded: false,
      error: "config unreadable",
    })
  })

  it("does not let a slower read overwrite a newer write", async () => {
    const { result } = await loadedHook()
    let releaseRead: () => void = () => {}
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        if (String(input) === "/api/default-model" && !init?.method) {
          const snapshot = stored
          return new Promise<Response>((resolve) => {
            releaseRead = () =>
              resolve(new Response(JSON.stringify({ selection: snapshot })))
          })
        }
        return fakeServer(input, init)
      }),
    )
    let read: Promise<void> = Promise.resolve()
    act(() => {
      read = result.current.refresh()
    })
    await act(async () => {
      await result.current.setDefault("primary")
    })
    await act(async () => {
      releaseRead()
      await read
    })
    expect(result.current.selection).toBe("primary")
    expect(result.current.loading).toBe(false)
  })
})
