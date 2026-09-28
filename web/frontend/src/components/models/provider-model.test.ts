import type { TFunction } from "i18next"
import { afterEach, describe, expect, it } from "vitest"

import type {
  ProviderInstance,
  ProviderRosterEntry,
} from "@/api/provider-instances"
import i18n from "@/i18n"

import {
  FREE_TEST_STORAGE_KEY,
  buildProviderCards,
  cardShelf,
  cardStatus,
  clearFreeTest,
  instanceDisplayName,
  instanceStatus,
  instanceSurfaces,
  isCardConnected,
  loadFreeTest,
  saveFreeTest,
  speaksOnly,
  surfacesText,
} from "./provider-model"

const instance = (extra: Partial<ProviderInstance> = {}): ProviderInstance => ({
  id: "inst",
  provider_kind: "openai",
  adapter: "openai-compatible",
  protocol: "openai",
  auth_configured: true,
  header_names: [],
  setting_names: [],
  state: "enabled",
  ...extra,
})

const entry = (extra: Partial<ProviderRosterEntry>): ProviderRosterEntry => ({
  id: "entry",
  display_name: "Entry",
  compatibility: "compatible",
  ...extra,
})

describe("instanceStatus", () => {
  it("is connected only when enabled and its credential is ready", () => {
    expect(
      instanceStatus(
        instance({ credential_kind: "api_key", credential_ready: true }),
      ),
    ).toBe("connected")
    expect(
      instanceStatus(
        instance({
          credential_kind: "none",
          credential_ready: true,
          state: "disabled",
        }),
      ),
    ).toBe("disabled")
  })

  it("names the missing credential before the disabled state", () => {
    const missing = (kind: ProviderInstance["credential_kind"]) =>
      instanceStatus(
        instance({
          credential_kind: kind,
          credential_ready: false,
          state: "disabled",
        }),
      )
    expect(missing("oauth")).toBe("needs_sign_in")
    expect(missing("token")).toBe("needs_token")
    expect(missing("api_key")).toBe("needs_key")
  })

  it("reads older servers from whether a credential is set", () => {
    expect(instanceStatus(instance({ auth_configured: false }))).toBe(
      "connected",
    )
    expect(
      instanceStatus(instance({ auth_configured: false, state: "disabled" })),
    ).toBe("disabled")
  })
})

describe("provider cards", () => {
  const roster = [
    entry({
      id: "openai",
      display_name: "OpenAI",
      requires_api_key: true,
      auth_methods: ["api_key"],
    }),
    entry({
      id: "free_one",
      display_name: "Free One",
      anonymous_automation: true,
    }),
    entry({ id: "ollama", display_name: "Ollama", auth_methods: ["none"] }),
    entry({
      id: "custom_openai",
      display_name: "Custom",
      auth_methods: ["api_key", "none"],
    }),
  ]

  it("gives an instance no roster entry names a card of its own, by display name", () => {
    const cards = buildProviderCards(roster, [
      instance({ id: "openai-2", provider_kind: "openai" }),
      instance({
        id: "ext-alpha",
        provider_kind: "extension",
        display_name: "Alpha Service",
        credential_kind: "none",
        credential_ready: true,
      }),
    ])
    expect(cards.map((card) => card.name)).toEqual([
      "OpenAI",
      "Free One",
      "Ollama",
      "Custom",
      "Alpha Service",
    ])
    expect(cards[0].instances.map((i) => i.id)).toEqual(["openai-2"])
    expect(instanceDisplayName(cards[4].instances[0])).toBe("Alpha Service")
  })

  it("files keyless connections with the free ones, never under API key", () => {
    const [, , , , alpha] = buildProviderCards(roster, [
      instance({
        id: "ext-alpha",
        provider_kind: "extension",
        credential_kind: "none",
        credential_ready: true,
      }),
    ])
    expect(cardShelf(alpha)).toBe("free")
    const custom = buildProviderCards(roster, [
      instance({
        id: "mine",
        provider_kind: "custom_openai",
        auth_configured: false,
      }),
    ]).find((card) => card.id === "custom_openai")!
    expect(cardShelf(custom)).toBe("free")
  })

  it("files unconnected entries by what they ask for", () => {
    const cards = buildProviderCards(roster, [])
    expect(cards.map(cardShelf)).toEqual(["apikey", "free", "local", "apikey"])
    expect(cards.map(cardStatus)).toEqual([
      undefined,
      undefined,
      undefined,
      undefined,
    ])
  })

  it("files sign-in and token connections together", () => {
    const [, , , , gamma] = buildProviderCards(roster, [
      instance({
        id: "ext-gamma",
        provider_kind: "extension",
        credential_kind: "oauth",
        credential_ready: false,
      }),
    ])
    expect(cardShelf(gamma)).toBe("account")
    expect(cardStatus(gamma)).toBe("needs_sign_in")
  })

  it("counts a card as connected only when it can be used", () => {
    const cards = buildProviderCards(roster, [
      instance({ id: "openai", provider_kind: "openai" }),
      instance({
        id: "ext-gamma",
        provider_kind: "extension",
        managed_by: "extension",
        credential_kind: "oauth",
        credential_ready: false,
      }),
      instance({
        id: "ext-beta",
        provider_kind: "extension",
        managed_by: "extension",
        credential_kind: "token",
        credential_ready: false,
        state: "disabled",
      }),
      instance({
        id: "paused",
        provider_kind: "ollama",
        credential_kind: "none",
        credential_ready: true,
        state: "disabled",
      }),
    ])
    expect(cards.filter(isCardConnected).map((card) => card.id)).toEqual([
      "openai",
    ])
  })
})

describe("surfaces", () => {
  const t = i18n.t.bind(i18n) as TFunction

  it("reads as labels, never as ids, each label once", () => {
    expect(surfacesText(["chat_completions", "responses"], t)).toBe("Chat")
    expect(surfacesText(["audio_speech"], t)).toBe("Text to speech")
    expect(
      surfacesText(["messages", "embeddings", "audio_transcriptions"], t),
    ).toBe("Chat, Embeddings, Speech to text")
    expect(surfacesText(["images", "videos"], t)).toBe("Images, Video")
    // A surface this version does not know reads in words.
    expect(surfacesText(["music_generation"], t)).toBe("Music generation")
    expect(surfacesText([], t)).toBe("")
  })

  it("of an instance come from its models, else from its protocol", () => {
    const managed = instance({
      protocol: "audio_speech",
      managed_by: "extension",
    })
    expect(instanceSurfaces(managed, [])).toEqual(["audio_speech"])
    expect(
      instanceSurfaces(managed, [
        { id: "a", surfaces: ["chat_completions"] },
        { id: "b", surfaces: ["chat_completions", "audio_speech"] },
      ]),
    ).toEqual(["chat_completions", "audio_speech"])
    // A wire protocol such as "openai" is no surface.
    expect(instanceSurfaces(instance({ protocol: "openai" }), [])).toEqual([])
  })

  it("tell a list of voices from any other catalog", () => {
    expect(
      speaksOnly([
        { id: "demo_voice", surfaces: ["audio_speech"] },
        { id: "other_voice", surfaces: ["audio_speech"] },
      ]),
    ).toBe(true)
    expect(
      speaksOnly([
        { id: "demo_voice", surfaces: ["audio_speech"] },
        { id: "chatty", surfaces: ["chat_completions"] },
      ]),
    ).toBe(false)
    // Unknown surfaces may serve chat, so they are no voices.
    expect(speaksOnly([{ id: "unknown" }])).toBe(false)
    expect(speaksOnly([])).toBe(false)
  })
})

describe("free test results", () => {
  afterEach(() => localStorage.clear())

  it("survive a reload until cleared", () => {
    expect(loadFreeTest()).toBeNull()
    saveFreeTest({
      at: "2026-01-01T00:00:00Z",
      verified: 1,
      outcomes: [{ registry_id: "a", provider_id: "a", status: "verified" }],
    })
    expect(loadFreeTest()?.outcomes).toHaveLength(1)
    clearFreeTest()
    expect(loadFreeTest()).toBeNull()
  })

  it("ignores stored data that is not a result", () => {
    localStorage.setItem(FREE_TEST_STORAGE_KEY, "{not json")
    expect(loadFreeTest()).toBeNull()
    localStorage.setItem(FREE_TEST_STORAGE_KEY, JSON.stringify({ at: 1 }))
    expect(loadFreeTest()).toBeNull()
  })
})
