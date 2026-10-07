import type { TFunction } from "i18next"

import type {
  AutoConnectFreeResult,
  CredentialKind,
  ProviderCatalogModel,
  ProviderInstance,
  ProviderRosterEntry,
} from "@/api/provider-instances"

/** The instance's human name, else its id. */
export function instanceDisplayName(
  instance: Pick<ProviderInstance, "id" | "display_name">,
): string {
  return instance.display_name?.trim() || instance.id
}

/**
 * Whether the extension serves the instance: its address and sign-in come
 * from the extension, so its card offers the provider's own sign-in instead
 * of the connect dialog.
 */
export function isExtensionManaged(
  instance: Pick<ProviderInstance, "managed_by">,
): boolean {
  return instance.managed_by === "extension"
}

/**
 * What the instance signs in with. A server that does not report it yet is
 * read from whether a credential is set.
 */
export function instanceCredentialKind(
  instance: Pick<ProviderInstance, "credential_kind" | "auth_configured">,
): CredentialKind {
  return (
    instance.credential_kind ?? (instance.auth_configured ? "api_key" : "none")
  )
}

/** Whether the credential the instance needs is in place. */
export function instanceCredentialReady(
  instance: Pick<
    ProviderInstance,
    "credential_kind" | "credential_ready" | "auth_configured"
  >,
): boolean {
  if (typeof instance.credential_ready === "boolean")
    return instance.credential_ready
  return instanceCredentialKind(instance) === "none" || instance.auth_configured
}

export type InstanceStatus =
  "connected" | "needs_sign_in" | "needs_token" | "needs_key" | "disabled"

/**
 * What an instance can do right now. Only an enabled instance whose
 * credential is ready is connected; a missing credential is named before a
 * disabled state because it is what the owner has to fix.
 */
export function instanceStatus(instance: ProviderInstance): InstanceStatus {
  if (!instanceCredentialReady(instance)) {
    switch (instanceCredentialKind(instance)) {
      case "oauth":
        return "needs_sign_in"
      case "token":
        return "needs_token"
      default:
        return "needs_key"
    }
  }
  return instance.state === "enabled" ? "connected" : "disabled"
}

/** One card of the provider list: a provider and the instances it has. */
export interface ProviderCard {
  id: string
  name: string
  description?: string
  /** The roster entry, absent for an instance no roster entry names. */
  roster?: ProviderRosterEntry
  instances: ProviderInstance[]
}

const matchesEntry = (instance: ProviderInstance, entry: ProviderRosterEntry) =>
  instance.provider_kind === entry.id || instance.id === entry.id

/**
 * The provider list: every roster entry with its instances, then a card of
 * its own for each instance no roster entry names, such as one an extension
 * serves.
 */
export function buildProviderCards(
  roster: readonly ProviderRosterEntry[],
  instances: readonly ProviderInstance[],
): ProviderCard[] {
  const cards: ProviderCard[] = []
  const seen = new Set<string>()
  for (const entry of roster) {
    if (seen.has(entry.id)) continue
    seen.add(entry.id)
    cards.push({
      id: entry.id,
      name: entry.display_name || entry.label || entry.id,
      description: entry.description,
      roster: entry,
      instances: instances.filter((instance) => matchesEntry(instance, entry)),
    })
  }
  for (const instance of instances) {
    if (roster.some((entry) => matchesEntry(instance, entry))) continue
    if (seen.has(instance.id)) continue
    seen.add(instance.id)
    cards.push({
      id: instance.id,
      name: instanceDisplayName(instance),
      instances: [instance],
    })
  }
  return cards
}

/** A card's status: the first connected instance's, else its first one's. */
export function cardStatus(card: ProviderCard): InstanceStatus | undefined {
  if (card.instances.length === 0) return undefined
  const statuses = card.instances.map(instanceStatus)
  return statuses.includes("connected") ? "connected" : statuses[0]
}

/**
 * Whether a card can be used now: one of its instances is enabled and has
 * its credential. A provider that still needs a sign-in, a token or a key is
 * not connected, however it was added.
 */
export function isCardConnected(card: ProviderCard): boolean {
  return cardStatus(card) === "connected"
}

/** The model surfaces llmgw-core defines; each has a models.surface label. */
export const KNOWN_SURFACES = [
  "chat_completions",
  "responses",
  "messages",
  "embeddings",
  "audio_transcriptions",
  "audio_speech",
  "images",
  "videos",
] as const

export type KnownSurface = (typeof KNOWN_SURFACES)[number]

export function isKnownSurface(surface: string): surface is KnownSurface {
  return (KNOWN_SURFACES as readonly string[]).includes(surface)
}

/**
 * Surfaces as a person reads them, e.g. "Chat, Text to speech": each by its
 * label, a label several surfaces share once, and a surface this version
 * does not know in words rather than as its id.
 */
export function surfacesText(
  surfaces: readonly string[],
  t: TFunction,
): string {
  const labels = surfaces.map((surface) => {
    if (isKnownSurface(surface)) return t(`models.surface.${surface}`)
    const words = surface.trim().replace(/[_-]+/g, " ").trim()
    return words ? words[0].toUpperCase() + words.slice(1) : ""
  })
  return [...new Set(labels.filter(Boolean))].join(", ")
}

/**
 * What an instance's models serve, in catalog order. Before its catalog
 * loads, an instance the extension serves serves its protocol, which is a
 * surface, as does any instance whose protocol names one.
 */
export function instanceSurfaces(
  instance: Pick<ProviderInstance, "protocol" | "managed_by">,
  models: readonly ProviderCatalogModel[],
): string[] {
  const surfaces = [...new Set(models.flatMap((model) => model.surfaces ?? []))]
  if (surfaces.length > 0) return surfaces
  const protocol = instance.protocol.trim()
  return protocol && (isExtensionManaged(instance) || isKnownSurface(protocol))
    ? [protocol]
    : []
}

/**
 * Whether every model of a catalog only speaks text aloud, as the voices of
 * a text-to-speech provider do.
 */
export function speaksOnly(models: readonly ProviderCatalogModel[]): boolean {
  return (
    models.length > 0 &&
    models.every(
      (model) =>
        (model.surfaces?.length ?? 0) > 0 &&
        (model.surfaces ?? []).every((surface) => surface === "audio_speech"),
    )
  )
}

export type ShelfKey = "free" | "apikey" | "account" | "local" | "other"

export const SHELF_ORDER: readonly ShelfKey[] = [
  "free",
  "apikey",
  "account",
  "local",
  "other",
]

const LOCAL_HINTS = ["ollama", "lmstudio", "localai", "local"]

/** Whether a card is a model server on this machine or network. */
export function isLocalCard(card: ProviderCard): boolean {
  const names = [card.id, ...card.instances.map((i) => i.provider_kind)].map(
    (name) => name.toLowerCase(),
  )
  return names.some((name) => LOCAL_HINTS.some((hint) => name.includes(hint)))
}

/**
 * Where a card is listed. A connected card goes by what its instances sign
 * in with, so a provider that needs no key is free whether a free test or an
 * extension added it; an unconnected one by what its roster entry asks for.
 */
export function cardShelf(card: ProviderCard): ShelfKey {
  if (isLocalCard(card)) return "local"
  if (card.instances.length > 0) {
    const kinds = card.instances.map(instanceCredentialKind)
    if (kinds.includes("none")) return "free"
    if (kinds.includes("api_key")) return "apikey"
    return "account"
  }
  const entry = card.roster
  if (!entry || entry.compatibility === "discovery_only") return "other"
  const methods = entry.auth_methods ?? []
  if (
    entry.anonymous_automation ||
    (methods.length > 0 && methods.every((method) => method === "none"))
  )
    return "free"
  if (entry.requires_api_key || methods.includes("api_key")) return "apikey"
  return "other"
}

/** Whether the connect dialog asks for an API key for this provider. */
export function takesAPIKey(entry: ProviderRosterEntry | undefined): boolean {
  if (!entry) return true
  const methods = entry.auth_methods ?? []
  return Boolean(entry.requires_api_key) || methods.includes("api_key")
}

/** A free provider test's results, kept until the next test or dismissal. */
export interface StoredFreeTest {
  /** When the test ran, RFC 3339. */
  at: string
  verified: number
  outcomes: AutoConnectFreeResult["outcomes"]
}

export const FREE_TEST_STORAGE_KEY = "compa:free-provider-test"

function storage(): Storage | undefined {
  try {
    return globalThis.localStorage
  } catch {
    return undefined
  }
}

export function loadFreeTest(): StoredFreeTest | null {
  try {
    const raw = storage()?.getItem(FREE_TEST_STORAGE_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw) as Partial<StoredFreeTest>
    if (!Array.isArray(parsed.outcomes) || typeof parsed.at !== "string")
      return null
    return {
      at: parsed.at,
      verified: typeof parsed.verified === "number" ? parsed.verified : 0,
      outcomes: parsed.outcomes,
    }
  } catch {
    return null
  }
}

export function saveFreeTest(result: StoredFreeTest) {
  try {
    storage()?.setItem(FREE_TEST_STORAGE_KEY, JSON.stringify(result))
  } catch {
    // Storage may be full or blocked; the results still show until reload.
  }
}

export function clearFreeTest() {
  try {
    storage()?.removeItem(FREE_TEST_STORAGE_KEY)
  } catch {
    // Nothing to clear.
  }
}
