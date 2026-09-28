import { afterEach, describe, expect, it, vi } from "vitest"

import {
  type ProviderInstanceInput,
  autoConnectFreeProviders,
  createProviderInstance,
  pingProviderInstance,
  providerInstanceRequestBody,
  servesChat,
  syncProviderCatalog,
  updateProviderInstance,
} from "./provider-instances"

const instance: ProviderInstanceInput = {
  id: "first",
  provider_kind: "openai",
  adapter: "openai-compatible",
  protocol: "openai",
  endpoint: "https://example.test/v1",
  state: "enabled",
}

const okFetch = () => {
  const fetchMock = vi
    .fn()
    .mockImplementation(() =>
      Promise.resolve(new Response("{}", { status: 200 })),
    )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}
const sentBody = (fetchMock: ReturnType<typeof okFetch>) =>
  JSON.parse(fetchMock.mock.calls[0][1].body)

describe("provider instance API", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("sends a strict empty catalog sync body", async () => {
    const fetchMock = okFetch()
    await syncProviderCatalog("first")
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/provider-instances/first/catalog/sync",
      expect.objectContaining({ body: "{}", method: "POST" }),
    )
  })

  it("omits write-only values when an edit does not replace them", async () => {
    const fetchMock = okFetch()
    await updateProviderInstance("first", instance)
    const body = sentBody(fetchMock)
    expect(body.write_only).toBeUndefined()
    expect(JSON.stringify(body)).not.toContain("secret")
  })

  it("nests intentionally changed sensitive values under write_only", async () => {
    const fetchMock = okFetch()
    await createProviderInstance({
      ...instance,
      write_only: {
        auth_connection_ref: "credential:first",
        api_key: "sk-fixture",
      },
    })
    const body = sentBody(fetchMock)
    expect(body.write_only).toEqual({
      auth_connection_ref: "credential:first",
      api_key: "sk-fixture",
    })
    expect(body.auth_connection_ref).toBeUndefined()
    expect(body.api_key).toBeUndefined()
  })

  it("sends runtime settings on create and update", async () => {
    const runtime = {
      proxy: "http://127.0.0.1:7890",
      request_timeout: 45,
      rpm: 20,
      streaming: true,
      thinking_level: "high" as const,
      max_tokens_field: "max_completion_tokens",
      tool_schema_transform: "simple",
      extra_body: { reasoning_split: true },
    }
    const fetchMock = okFetch()
    await createProviderInstance({ ...instance, runtime })
    await updateProviderInstance("first", { ...instance, runtime })
    expect(JSON.parse(fetchMock.mock.calls[0][1].body).runtime).toEqual(runtime)
    expect(fetchMock.mock.calls[1][0]).toBe("/api/provider-instances/first")
    expect(fetchMock.mock.calls[1][1].method).toBe("PUT")
    expect(JSON.parse(fetchMock.mock.calls[1][1].body).runtime).toEqual(runtime)
  })

  it("clears runtime settings on update but leaves a create without them", () => {
    expect(providerInstanceRequestBody(instance, "update").runtime).toEqual({})
    expect(
      providerInstanceRequestBody({ ...instance, runtime: {} }, "create"),
    ).not.toHaveProperty("runtime")
  })

  it("sends only the fields the server decodes", () => {
    const stored = {
      ...instance,
      auth_configured: true,
      header_names: [],
      setting_names: [],
    }
    expect(
      Object.keys(providerInstanceRequestBody(stored, "update")).sort(),
    ).toEqual([
      "adapter",
      "endpoint",
      "id",
      "protocol",
      "provider_kind",
      "runtime",
      "state",
    ])
  })

  it("posts to ping endpoint and auto-connect-free endpoint", async () => {
    const fetchMock = vi.fn().mockImplementation(() =>
      Promise.resolve(
        new Response(JSON.stringify({ ok: true, latency_ms: 42 }), {
          status: 200,
        }),
      ),
    )
    vi.stubGlobal("fetch", fetchMock)
    const pingRes = await pingProviderInstance("groq")
    expect(pingRes.ok).toBe(true)
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/provider-instances/groq/ping",
      expect.objectContaining({ method: "POST" }),
    )

    const autoRes = await autoConnectFreeProviders()
    expect(autoRes.ok).toBe(true)
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/provider-instances/auto-connect-free",
      expect.objectContaining({ method: "POST" }),
    )
  })
})

describe("servesChat", () => {
  it("offers models whose catalog reports a chat surface or none", () => {
    expect(servesChat(undefined)).toBe(true)
    expect(servesChat([])).toBe(true)
    expect(servesChat(["chat_completions"])).toBe(true)
    expect(servesChat(["messages"])).toBe(true)
    expect(servesChat(["audio_speech", "responses"])).toBe(true)
  })

  it("keeps speech-only and other non-chat models out of chat", () => {
    expect(servesChat(["audio_speech"])).toBe(false)
    expect(servesChat(["audio_transcriptions", "embeddings"])).toBe(false)
  })
})