import {
  type ProviderInstanceRuntime,
  THINKING_LEVELS,
  type ThinkingLevel,
} from "@/api/provider-instances"

/** The runtime settings as the editor holds them: text as typed. */
export interface RuntimeFormState {
  proxy: string
  requestTimeout: string
  rpm: string
  /**
   * Provider streaming as stored or as last switched. Undefined leaves it
   * unset, which the server treats as on; only false turns it off.
   */
  streaming: boolean | undefined
  /** Empty leaves the level to the agent. */
  thinkingLevel: ThinkingLevel | ""
  maxTokensField: string
  toolSchemaTransform: string
  /** A JSON object, or empty for none. */
  extraBody: string
}

export type RuntimeErrorCode =
  | "invalidProxy"
  | "nonNegativeInteger"
  | "fieldName"
  | "invalidJson"
  | "jsonObject"
  | "emptyKey"

export type RuntimeFormErrors = Partial<
  Record<
    "proxy" | "requestTimeout" | "rpm" | "maxTokensField" | "extraBody",
    RuntimeErrorCode
  >
>

export type RuntimeBuildResult =
  | { ok: true; runtime: ProviderInstanceRuntime }
  | { ok: false; errors: RuntimeFormErrors }

export const EMPTY_RUNTIME_FORM: RuntimeFormState = {
  proxy: "",
  requestTimeout: "",
  rpm: "",
  streaming: undefined,
  thinkingLevel: "",
  maxTokensField: "",
  toolSchemaTransform: "",
  extraBody: "",
}

/** Whether provider streaming is on: it is unless it was turned off. */
export function streamingOn(form: Pick<RuntimeFormState, "streaming">) {
  return form.streaming !== false
}

// The proxy schemes the server accepts.
const PROXY_SCHEMES = new Set(["http:", "https:", "socks5:", "socks5h:"])

function isThinkingLevel(value: string): value is ThinkingLevel {
  return (THINKING_LEVELS as readonly string[]).includes(value)
}

/** The editor state for an instance's stored runtime settings. */
export function runtimeFormFromSettings(
  runtime?: ProviderInstanceRuntime | null,
): RuntimeFormState {
  if (!runtime) return EMPTY_RUNTIME_FORM
  const level = runtime.thinking_level?.trim().toLowerCase() ?? ""
  const extraBody = runtime.extra_body
  return {
    proxy: runtime.proxy ?? "",
    requestTimeout: runtime.request_timeout
      ? String(runtime.request_timeout)
      : "",
    rpm: runtime.rpm ? String(runtime.rpm) : "",
    streaming:
      typeof runtime.streaming === "boolean" ? runtime.streaming : undefined,
    thinkingLevel: isThinkingLevel(level) ? level : "",
    maxTokensField: runtime.max_tokens_field ?? "",
    toolSchemaTransform: runtime.tool_schema_transform ?? "",
    extraBody:
      extraBody && Object.keys(extraBody).length > 0
        ? JSON.stringify(extraBody, null, 2)
        : "",
  }
}

type Parsed<T> = { value?: T; error?: RuntimeErrorCode }

// A count setting: a whole number of 0 or more, where 0 and blank both mean
// the provider default and are left out.
function parseCount(raw: string): Parsed<number> {
  const text = raw.trim()
  if (!text) return {}
  if (!/^\d+$/.test(text)) return { error: "nonNegativeInteger" }
  const value = Number(text)
  if (!Number.isSafeInteger(value)) return { error: "nonNegativeInteger" }
  return value > 0 ? { value } : {}
}

function parseProxy(raw: string): Parsed<string> {
  const text = raw.trim()
  if (!text) return {}
  try {
    const url = new URL(text)
    if (PROXY_SCHEMES.has(url.protocol) && url.host !== "")
      return { value: text }
  } catch {
    // Not a URL at all.
  }
  return { error: "invalidProxy" }
}

function parseFieldName(raw: string): Parsed<string> {
  const text = raw.trim()
  if (!text) return {}
  return /\s/.test(text) ? { error: "fieldName" } : { value: text }
}

function parseExtraBody(raw: string): Parsed<Record<string, unknown>> {
  const text = raw.trim()
  if (!text) return {}
  let parsed: unknown
  try {
    parsed = JSON.parse(text)
  } catch {
    return { error: "invalidJson" }
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed))
    return { error: "jsonObject" }
  const body = parsed as Record<string, unknown>
  const keys = Object.keys(body)
  if (keys.some((key) => key.trim() === "")) return { error: "emptyKey" }
  return keys.length > 0 ? { value: body } : {}
}

/**
 * Validates the editor state and builds the runtime settings to send: only
 * the fields that are set, in a stable order. Blank fields, a zero count,
 * streaming nobody set and an empty extra body are left out; streaming
 * turned off is sent, since unset means on.
 */
export function buildRuntimeSettings(
  form: RuntimeFormState,
): RuntimeBuildResult {
  const proxy = parseProxy(form.proxy)
  const requestTimeout = parseCount(form.requestTimeout)
  const rpm = parseCount(form.rpm)
  const maxTokensField = parseFieldName(form.maxTokensField)
  const extraBody = parseExtraBody(form.extraBody)

  const errors: RuntimeFormErrors = {}
  if (proxy.error) errors.proxy = proxy.error
  if (requestTimeout.error) errors.requestTimeout = requestTimeout.error
  if (rpm.error) errors.rpm = rpm.error
  if (maxTokensField.error) errors.maxTokensField = maxTokensField.error
  if (extraBody.error) errors.extraBody = extraBody.error
  if (Object.keys(errors).length > 0) return { ok: false, errors }

  const runtime: ProviderInstanceRuntime = {}
  if (proxy.value) runtime.proxy = proxy.value
  if (requestTimeout.value) runtime.request_timeout = requestTimeout.value
  if (rpm.value) runtime.rpm = rpm.value
  if (form.streaming !== undefined) runtime.streaming = form.streaming
  if (form.thinkingLevel) runtime.thinking_level = form.thinkingLevel
  if (maxTokensField.value) runtime.max_tokens_field = maxTokensField.value
  const toolSchemaTransform = form.toolSchemaTransform.trim()
  if (toolSchemaTransform) runtime.tool_schema_transform = toolSchemaTransform
  if (extraBody.value) runtime.extra_body = extraBody.value
  return { ok: true, runtime }
}
