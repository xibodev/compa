import { renderHook, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { useChatSelections } from "./use-chat-selections"

describe("useChatSelections", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("loads only authoritative targets and routes", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = String(input)
      if (path === "/api/provider-targets")
        return Promise.resolve(
          new Response(
            JSON.stringify({ targets: [{ target: "first/model" }] }),
          ),
        )
      if (path === "/api/model-routes")
        return Promise.resolve(
          new Response(
            JSON.stringify({
              routes: [{ name: "primary", targets: ["first/model"] }],
            }),
          ),
        )
      return Promise.resolve(new Response("missing", { status: 404 }))
    })
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useChatSelections())
    await waitFor(() => expect(result.current.targets).toHaveLength(1))
    expect(result.current.routes[0]?.name).toBe("primary")
    expect(fetchMock.mock.calls.map(([input]) => String(input))).toEqual([
      "/api/provider-targets",
      "/api/model-routes",
    ])
  })

  it("loads targets without a running gateway", async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const path = String(input)
      if (path === "/api/provider-targets")
        return Promise.resolve(
          new Response(
            JSON.stringify({ targets: [{ target: "remote-provider/chat-model" }] }),
          ),
        )
      if (path === "/api/model-routes")
        return Promise.resolve(new Response(JSON.stringify({ routes: [] })))
      return Promise.resolve(new Response("missing", { status: 404 }))
    })
    vi.stubGlobal("fetch", fetchMock)

    const { result } = renderHook(() => useChatSelections())
    await waitFor(() => expect(result.current.targets).toHaveLength(1))
    expect(result.current.targets[0]?.target).toBe(
      "remote-provider/chat-model",
    )
  })

  it("retains targets when route discovery fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        if (String(input) === "/api/provider-targets")
          return Promise.resolve(
            new Response(JSON.stringify({ targets: [{ target: "first/model" }] })),
          )
        return Promise.resolve(new Response("failed", { status: 500 }))
      }),
    )
    const { result } = renderHook(() => useChatSelections())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.targets).toHaveLength(1)
    expect(result.current.error).not.toBe("")
  })
})
