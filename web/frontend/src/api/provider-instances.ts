import { HttpError, launcherFetch } from "@/api/http"

export type InstanceState = "enabled" | "disabled"

/** The extended-thinking levels a provider instance can request. */
export const THINKING_LEVELS = [
  "off",
  "low",
  "medium",
  "high",
  "xhigh",
  "adaptive",
] as const

export type ThinkingLevel = (typeof THINKING_LEVELS)[number]

/**
 * The request settings every model call on a provider instance runs with.
 * An unset field uses the provider default.
 */
export interface ProviderInstanceRuntime {
  /** An http, https, socks5 or socks5h proxy URL. */
  proxy?: string
  /** Seconds a request may take; 0 uses the provider default. */
  request_timeout?: number
  /** Requests per minute; 0 is unlimited. */
  rpm?: number
  /** Provider streaming; unset leaves it on, only false turns it off. */
  streaming?: boolean
  thinking_level?: ThinkingLevel
  /** The request field carrying the token limit, e.g. max_completion_tokens. */
  max_tokens_field?: string
  /** "off" (or unset) or "simple". */
  tool_schema_transform?: string
  /** Fields merged into every request body. */
  extra_body?: Record<string, unknown>
}

/** What a provider instance signs in with. */
export type CredentialKind = "none" | "api_key" | "token" | "oauth"

export interface ProviderInstance {
  id: string
  provider_kind: string
  adapter: string
  protocol: string
  endpoint?: string
  auth_configured: boolean
  header_names: string[]
  setting_names: string[]
  runtime?: ProviderInstanceRuntime
  state: InstanceState
  /** The instance's human name, e.g. its provider's label. */
  display_name?: string
  /** "extension" when the extension serves the instance and owns its sign-in. */
  managed_by?: "extension" | ""
  credential_kind?: CredentialKind
  /** Whether the credential the instance needs is in place. */
  credential_ready?: boolean
}

export interface ProviderInstanceInput {
  id: string
  provider_kind: string
  adapter: string
  protocol: string
  endpoint?: string
  state: InstanceState
  runtime?: ProviderInstanceRuntime
  write_only?: {
    auth_connection_ref?: string
    api_key?: string
  }
}

export interface ProviderTarget {
  target: string
  instance_id: string
  model_id: string
  provider_kind: string
  owned_by?: string
  display_name?: string
  /** The model's display name. */
  label?: string
  /** The display name of the instance serving the model. */
  instance_label?: string
  /** llmgw-core surfaces the catalog reports, e.g. "audio_transcriptions". */
  surfaces?: string[]
  fetched_at: string
}

/** Surfaces a model serves chat on: natively, over Messages or over Responses. */
const CHAT_SURFACES = new Set(["chat_completions", "messages", "responses"])

/**
 * Whether a catalog model can be selected for chat. A catalog that reports
 * no surfaces leaves them unknown, so such a model stays selectable; a
 * speech-only model (e.g. surfaces ["audio_speech"]) is not.
 */
export function servesChat(surfaces?: readonly string[]): boolean {
  if (!surfaces || surfaces.length === 0) return true
  return surfaces.some((surface) => CHAT_SURFACES.has(surface))
}
export interface ProviderCatalogModel {
  id: string
  owned_by?: string
  display_name?: string
  /** llmgw-core surfaces the catalog reports, e.g. "audio_speech". */
  surfaces?: string[]
}

export interface ProviderInstanceCatalog {
  instance_id: string
  provider_kind: string
  models: ProviderCatalogModel[]
  fetched_at: string
}

export interface ModelRoute {
  name: string
  targets: string[]
}

export interface ProviderRosterEntry {
  id: string
  display_name: string
  label?: string
  description?: string
  categories?: string[]
  adapter?: string
  protocol?: string
  default_endpoint?: string
  compatibility: "compatible" | "discovery_only"
  auth_methods?: string[]
  requires_api_key?: boolean
  requires_base_url?: boolean
  anonymous_automation?: boolean
  onboarding_fields?: string[]
  configured?: boolean
  instance_count?: number
  configured_instances?: string[]
}

export interface PingResult {
  ok: boolean
  instance_id: string
  latency_ms: number
  model_count?: number
  status: "reachable" | "unreachable" | "configured"
  error?: string
}

export interface AutoConnectFreeResult {
  ok: boolean
  total: number
  catalog_discovered: number
  verified: number
  instances: string[]
  outcomes: {
    registry_id: string
    provider_id: string
    /**
     * "verified" answered and joined Chat; "connected" listed its models but
     * its test answer failed; "failed" could not be used at all.
     */
    status: "verified" | "connected" | "failed"
    models?: string[]
    probe_model?: string
    latency_ms?: number
    /** Why it was not added; "no_model" lists no model Compa can enroll. */
    error_class?:
      | "rate_limited"
      | "auth_required"
      | "forbidden"
      | "probe_failed"
      | "no_model"
    /** A short plain sentence, never an upstream body. */
    error?: string
  }[]
  default_model?: string
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await launcherFetch(path, init)
  if (!response.ok)
    throw new HttpError(
      (await response.text()) || response.statusText,
      response.status,
    )
  return response.json() as Promise<T>
}

const json = (body: unknown): RequestInit => ({
  method: "POST",
  headers: { "Content-Type": "application/json" },
  body: JSON.stringify(body),
})

/**
 * The JSON body of a provider instance create or update. The server decodes
 * it strictly, so only known fields are sent. An update replaces the stored
 * runtime settings as a whole and therefore always carries them, empty to
 * clear them; a create carries only settings it sets. write_only is sent
 * only when a secret is being replaced.
 */
export function providerInstanceRequestBody(
  input: ProviderInstanceInput,
  mode: "create" | "update",
) {
  const body: Record<string, unknown> = {
    id: input.id,
    provider_kind: input.provider_kind,
    adapter: input.adapter,
    protocol: input.protocol,
    state: input.state,
  }
  if (input.endpoint !== undefined) body.endpoint = input.endpoint
  const runtime = input.runtime ?? {}
  if (mode === "update" || Object.keys(runtime).length > 0)
    body.runtime = runtime
  if (input.write_only && Object.keys(input.write_only).length > 0)
    body.write_only = input.write_only
  return body
}

export const listProviderInstances = () =>
  request<{ instances: ProviderInstance[] }>("/api/provider-instances")
export const listProviderRoster = () =>
  request<{ providers: ProviderRosterEntry[] }>("/api/provider-roster")
export const createProviderInstance = (input: ProviderInstanceInput) =>
  request(
    "/api/provider-instances",
    json(providerInstanceRequestBody(input, "create")),
  )
export const updateProviderInstance = (
  id: string,
  input: ProviderInstanceInput,
) =>
  request(`/api/provider-instances/${encodeURIComponent(id)}`, {
    ...json(providerInstanceRequestBody(input, "update")),
    method: "PUT",
  })
export const deleteProviderInstance = (id: string) =>
  request(`/api/provider-instances/${encodeURIComponent(id)}`, {
    method: "DELETE",
  })
export const syncProviderCatalog = (id: string) =>
  request<{
    instance_id: string
    models?: ProviderCatalogModel[]
    total?: number
  }>(`/api/provider-instances/${encodeURIComponent(id)}/catalog/sync`, json({}))
export const pingProviderInstance = (id: string) =>
  request<PingResult>(
    `/api/provider-instances/${encodeURIComponent(id)}/ping`,
    json({}),
  )
export const autoConnectFreeProviders = () =>
  request<AutoConnectFreeResult>(
    "/api/provider-instances/auto-connect-free",
    json({}),
  )
export const getActiveModels = () =>
  request<{ active_models: string[]; total: number }>("/api/active-models")
export const addActiveModel = (target: string) =>
  request<{ active_models: string[]; status: string }>(
    "/api/active-models/add",
    json({ target }),
  )
export const removeActiveModel = (target: string) =>
  request<{ active_models: string[]; status: string }>(
    "/api/active-models/remove",
    json({ target }),
  )
export const listProviderTargets = (all = false) =>
  request<{ targets: ProviderTarget[] }>(
    `/api/provider-targets${all ? "?all=true" : ""}`,
  )
export const listProviderInstanceCatalogs = () =>
  request<{ catalogs: ProviderInstanceCatalog[] }>(
    "/api/provider-instances/catalogs",
  )
export const listModelRoutes = () =>
  request<{ routes: ModelRoute[] }>("/api/model-routes")
export const createModelRoute = (route: ModelRoute) =>
  request("/api/model-routes", json(route))
export const updateModelRoute = (name: string, route: ModelRoute) =>
  request(`/api/model-routes/${encodeURIComponent(name)}`, {
    ...json(route),
    method: "PUT",
  })
export const deleteModelRoute = (name: string) =>
  request(`/api/model-routes/${encodeURIComponent(name)}`, { method: "DELETE" })
