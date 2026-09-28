import { launcherFetch } from "@/api/http"

const EXTENSION_API = "/api/extension"

export type ExtensionConnectionStatus =
  "not_configured" | "connected" | "unreachable"
export type ExtensionCredentialKind = "none" | "token" | "oauth"
export type ExtensionSignInMethod = "device" | "manual"

export interface ExtensionProvider {
  id: string
  name: string
  credential: ExtensionCredentialKind
  methods?: ExtensionSignInMethod[]
  supported: boolean
  reason?: string
  instance_id?: string
  connected: boolean
}

/** Where the extension listens unless it is started elsewhere. */
export const DEFAULT_EXTENSION_URL = "http://127.0.0.1:18888"

/**
 * What a provider the extension serves can do now, as one state: a keyless
 * one is ready once connected, one taking a token or a sign-in is connected
 * once it has it, and until then it needs it.
 */
export type ExtensionProviderState =
  | "ready"
  | "not_ready"
  | "connected"
  | "needs_token"
  | "needs_sign_in"
  | "unsupported"

export function extensionProviderState(
  provider: Pick<ExtensionProvider, "supported" | "credential" | "connected">,
): ExtensionProviderState {
  if (!provider.supported) return "unsupported"
  switch (provider.credential) {
    case "none":
      return provider.connected ? "ready" : "not_ready"
    case "token":
      return provider.connected ? "connected" : "needs_token"
    default:
      return provider.connected ? "connected" : "needs_sign_in"
  }
}

export interface ExtensionStatus {
  url?: string
  has_secret: boolean
  status: ExtensionConnectionStatus
  error?: string
  version?: string
  providers: ExtensionProvider[]
}

export interface ExtensionConfigRequest {
  url: string
  secret?: string
  keep_secret?: boolean
}

export interface SignInFlow {
  flow_id: string
  method: ExtensionSignInMethod
  status: "pending"
  authorization_url?: string
  user_code?: string
  verification_uri?: string
  verification_uri_complete?: string
  interval_seconds?: number
  expires_at?: string
}

export type SignInPollStatus =
  "pending" | "slow_down" | "approved" | "denied" | "expired"

export interface SignInPollResult {
  status: SignInPollStatus
  error?: string
  [key: string]: unknown
}

async function request<T>(path: string, options?: RequestInit): Promise<T> {
  const res = await launcherFetch(path, options)
  if (!res.ok) {
    const message = await res.text()
    throw new Error(message || `API error: ${res.status} ${res.statusText}`)
  }
  return res.json() as Promise<T>
}

function jsonBody(method: string, body?: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  }
}

const providerPath = (id: string) =>
  `${EXTENSION_API}/providers/${encodeURIComponent(id)}`
const flowPath = (flowID: string) =>
  `${EXTENSION_API}/signin/${encodeURIComponent(flowID)}`

export function getExtensionStatus(): Promise<ExtensionStatus> {
  return request<ExtensionStatus>(EXTENSION_API)
}

export function configureExtension(
  payload: ExtensionConfigRequest,
): Promise<ExtensionStatus> {
  return request<ExtensionStatus>(EXTENSION_API, jsonBody("PUT", payload))
}

export function disconnectExtension(): Promise<{ status: string }> {
  return request<{ status: string }>(EXTENSION_API, { method: "DELETE" })
}

export function saveExtensionToken(
  providerID: string,
  token: string,
): Promise<{ status: string; instance_id?: string }> {
  return request(
    `${providerPath(providerID)}/token`,
    jsonBody("POST", { token }),
  )
}

export function removeExtensionCredential(
  providerID: string,
): Promise<{ status: string }> {
  return request(`${providerPath(providerID)}/credential`, { method: "DELETE" })
}

export function startExtensionSignIn(
  providerID: string,
  method: ExtensionSignInMethod,
): Promise<SignInFlow> {
  return request(
    `${providerPath(providerID)}/signin`,
    jsonBody("POST", { method }),
  )
}

export function pollExtensionSignIn(flowID: string): Promise<SignInPollResult> {
  return request(`${flowPath(flowID)}/poll`, { method: "POST" })
}

export function completeExtensionSignIn(
  flowID: string,
  code: string,
): Promise<{ status: string }> {
  return request(`${flowPath(flowID)}/complete`, jsonBody("POST", { code }))
}
