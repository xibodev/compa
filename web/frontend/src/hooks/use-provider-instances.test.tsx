import { renderHook, waitFor } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { useProviderInstances } from "./use-provider-instances"

const response = (body: unknown) =>
  Promise.resolve(new Response(JSON.stringify(body), { status: 200 }))

describe("useProviderInstances", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("loads the authoritative management collections", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        switch (String(input)) {
          case "/api/provider-instances":
            return response({ instances: [{ id: "first" }] })
          case "/api/provider-targets?all=true":
            return response({ targets: [{ target: "first/model" }] })
          case "/api/provider-instances/catalogs":
            return response({
              catalogs: [{ instance_id: "first", models: [] }],
            })
          case "/api/model-routes":
            return response({ routes: [{ name: "primary", targets: [] }] })
          case "/api/provider-roster":
            return response({ providers: [{ id: "unknown" }] })
          default:
            return response({})
        }
      }),
    )

    const { result } = renderHook(() => useProviderInstances())
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.instances[0]?.id).toBe("first")
    expect(result.current.targets[0]?.target).toBe("first/model")
    expect(result.current.catalogs[0]?.instance_id).toBe("first")
    expect(result.current.routes[0]?.name).toBe("primary")
    expect(result.current.roster[0]?.id).toBe("unknown")
  })
})
