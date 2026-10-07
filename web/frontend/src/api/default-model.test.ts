import { afterEach, describe, expect, it, vi } from "vitest"

import { getDefaultModel, setDefaultModel } from "./default-model"

describe("default model API", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("reads the default selection", async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(
        new Response(JSON.stringify({ selection: "first/model" })),
      )
    vi.stubGlobal("fetch", fetchMock)
    await expect(getDefaultModel()).resolves.toEqual({
      selection: "first/model",
    })
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/default-model",
      expect.objectContaining({ credentials: "same-origin" }),
    )
  })

  it("treats a missing selection as no default", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(new Response(JSON.stringify({}))),
    )
    await expect(getDefaultModel()).resolves.toEqual({ selection: "" })
  })

  it("puts a trimmed selection, and an empty one to clear", async () => {
    const fetchMock = vi.fn((_input: RequestInfo | URL, init?: RequestInit) =>
      Promise.resolve(new Response(String(init?.body))),
    )
    vi.stubGlobal("fetch", fetchMock)
    await expect(setDefaultModel(" primary ")).resolves.toEqual({
      selection: "primary",
    })
    await expect(setDefaultModel("")).resolves.toEqual({ selection: "" })
    expect(
      fetchMock.mock.calls.map(([path, init]) => [path, init?.method]),
    ).toEqual([
      ["/api/default-model", "PUT"],
      ["/api/default-model", "PUT"],
    ])
    expect(fetchMock.mock.calls.map(([, init]) => init?.body)).toEqual([
      JSON.stringify({ selection: "primary" }),
      JSON.stringify({ selection: "" }),
    ])
  })

  it("surfaces the server's plain-text rejection", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn().mockResolvedValue(
        new Response('selection "gone/model" does not resolve\n', {
          status: 400,
        }),
      ),
    )
    await expect(setDefaultModel("gone/model")).rejects.toThrow(
      'selection "gone/model" does not resolve',
    )
  })
})
