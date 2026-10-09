import { renderHook, waitFor } from "@testing-library/react"
import type { TFunction } from "i18next"
import { afterEach, describe, expect, it, vi } from "vitest"

import i18n from "@/i18n"

import { useChannelList } from "./use-channel-list"

const reply = (value: unknown) =>
  Promise.resolve(new Response(JSON.stringify(value), { status: 200 }))

const CATALOG = {
  channels: [
    { name: "telegram", config_key: "telegram" },
    { name: "discord", config_key: "discord" },
    { name: "whatsapp", config_key: "whatsapp" },
    { name: "web", config_key: "web" },
  ],
}

function stubServer(config: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input)
      if (path === "/api/channels/catalog") return reply(CATALOG)
      if (path === "/api/config") return reply(config)
      return Promise.resolve(new Response("missing", { status: 404 }))
    }),
  )
}

const renderList = () =>
  renderHook(() =>
    useChannelList({ language: "en", t: i18n.t.bind(i18n) as TFunction }),
  )

describe("useChannelList", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("reads each channel's switch from channel_list, as the config has it", async () => {
    stubServer({
      channel_list: {
        telegram: { enabled: true, type: "telegram", settings: {} },
        discord: { enabled: false, type: "discord" },
        whatsapp: { enabled: true, type: "whatsapp" },
        web: { enabled: true, type: "web" },
      },
    })
    const { result } = renderList()
    await waitFor(() => expect(result.current.loading).toBe(false))
    const enabled = Object.fromEntries(
      result.current.channels.map((channel) => [channel.key, channel.enabled]),
    )
    expect(enabled).toEqual({
      telegram: true,
      discord: false,
      whatsapp: true,
    })
    // Enabled channels come first.
    expect(result.current.channels.slice(0, 2).map((item) => item.key)).toEqual(
      expect.arrayContaining(["telegram", "whatsapp"]),
    )
  })

  it("leaves out the browser chat's own channel", async () => {
    stubServer({ channel_list: { web: { enabled: true, type: "web" } } })
    const { result } = renderList()
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.channels.map((channel) => channel.key)).not.toContain(
      "web",
    )
    expect(result.current.channels).toHaveLength(3)
  })

  it("lists every channel as off when the config cannot be read", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        String(input) === "/api/channels/catalog"
          ? reply(CATALOG)
          : Promise.resolve(new Response("boom", { status: 500 })),
      ),
    )
    const { result } = renderList()
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.error).toBe("")
    expect(result.current.channels.every((channel) => !channel.enabled)).toBe(
      true,
    )
  })
})
