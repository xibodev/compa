import { describe, expect, it } from "vitest"

import {
  EMPTY_RUNTIME_FORM,
  type RuntimeFormState,
  buildRuntimeSettings,
  runtimeFormFromSettings,
  streamingOn,
} from "./runtime-settings"

const form = (overrides: Partial<RuntimeFormState>): RuntimeFormState => ({
  ...EMPTY_RUNTIME_FORM,
  ...overrides,
})

const build = (overrides: Partial<RuntimeFormState>) =>
  buildRuntimeSettings(form(overrides))

describe("buildRuntimeSettings", () => {
  it("sends nothing for an untouched form", () => {
    expect(buildRuntimeSettings(EMPTY_RUNTIME_FORM)).toEqual({
      ok: true,
      runtime: {},
    })
  })

  it("serializes every field that is set", () => {
    expect(
      build({
        proxy: " socks5://127.0.0.1:1080 ",
        requestTimeout: "30",
        rpm: " 60 ",
        streaming: true,
        thinkingLevel: "xhigh",
        maxTokensField: " max_completion_tokens ",
        toolSchemaTransform: " simple ",
        extraBody: '{"reasoning_split": true, "nested": {"a": [1, 2]}}',
      }),
    ).toEqual({
      ok: true,
      runtime: {
        proxy: "socks5://127.0.0.1:1080",
        request_timeout: 30,
        rpm: 60,
        streaming: true,
        thinking_level: "xhigh",
        max_tokens_field: "max_completion_tokens",
        tool_schema_transform: "simple",
        extra_body: { reasoning_split: true, nested: { a: [1, 2] } },
      },
    })
  })

  it("leaves out defaults: blank text, zero counts, streaming unset, an empty body", () => {
    expect(
      build({
        proxy: "   ",
        requestTimeout: "0",
        rpm: "",
        streaming: undefined,
        thinkingLevel: "",
        maxTokensField: " ",
        toolSchemaTransform: "",
        extraBody: " {} ",
      }),
    ).toEqual({ ok: true, runtime: {} })
  })

  it("sends streaming turned off, since unset means on", () => {
    expect(build({ streaming: false })).toEqual({
      ok: true,
      runtime: { streaming: false },
    })
    expect(build({ streaming: true })).toEqual({
      ok: true,
      runtime: { streaming: true },
    })
  })

  it.each([
    ["not json", "invalidJson"],
    ['{"a": 1,}', "invalidJson"],
    ["[1, 2]", "jsonObject"],
    ["null", "jsonObject"],
    ["42", "jsonObject"],
    ['"text"', "jsonObject"],
    ['{" ": true}', "emptyKey"],
  ])("rejects extra body %s", (extraBody, code) => {
    expect(build({ extraBody })).toEqual({
      ok: false,
      errors: { extraBody: code },
    })
  })

  it.each(["-1", "1.5", "1e3", "ten", "99999999999999999999"])(
    "rejects count %s",
    (value) => {
      expect(build({ requestTimeout: value, rpm: value })).toEqual({
        ok: false,
        errors: {
          requestTimeout: "nonNegativeInteger",
          rpm: "nonNegativeInteger",
        },
      })
    },
  )

  it.each([
    "http://127.0.0.1:7890",
    "https://user:pass@proxy.example.test:8443",
    "socks5://127.0.0.1:1080",
    "socks5h://proxy.local:1080",
  ])("accepts proxy %s", (proxy) => {
    expect(build({ proxy })).toEqual({ ok: true, runtime: { proxy } })
  })

  it.each(["127.0.0.1:7890", "localhost:8080", "ftp://proxy:21", "http://"])(
    "rejects proxy %s",
    (proxy) => {
      expect(build({ proxy })).toEqual({
        ok: false,
        errors: { proxy: "invalidProxy" },
      })
    },
  )

  it("rejects a max tokens field with spaces", () => {
    expect(build({ maxTokensField: "max tokens" })).toEqual({
      ok: false,
      errors: { maxTokensField: "fieldName" },
    })
  })

  it("reports every invalid field at once", () => {
    const result = build({
      proxy: "nope",
      rpm: "-2",
      extraBody: "{",
    })
    expect(result.ok).toBe(false)
    expect(!result.ok && result.errors).toEqual({
      proxy: "invalidProxy",
      rpm: "nonNegativeInteger",
      extraBody: "invalidJson",
    })
  })
})

describe("runtimeFormFromSettings", () => {
  it("starts empty without stored settings", () => {
    expect(runtimeFormFromSettings(undefined)).toEqual(EMPTY_RUNTIME_FORM)
    expect(runtimeFormFromSettings({})).toEqual(EMPTY_RUNTIME_FORM)
  })

  it("reads unset streaming as on and keeps it unset", () => {
    const editable = runtimeFormFromSettings({ rpm: 30 })
    expect(editable.streaming).toBeUndefined()
    expect(streamingOn(editable)).toBe(true)
    expect(buildRuntimeSettings(editable)).toEqual({
      ok: true,
      runtime: { rpm: 30 },
    })
    expect(streamingOn(runtimeFormFromSettings({ streaming: false }))).toBe(
      false,
    )
  })

  it("round-trips stored settings through the editor", () => {
    const runtime = {
      proxy: "http://127.0.0.1:7890",
      request_timeout: 120,
      rpm: 30,
      streaming: true,
      thinking_level: "adaptive" as const,
      max_tokens_field: "max_completion_tokens",
      tool_schema_transform: "simple",
      extra_body: { reasoning_split: true },
    }
    const editable = runtimeFormFromSettings(runtime)
    expect(editable.extraBody).toBe('{\n  "reasoning_split": true\n}')
    expect(buildRuntimeSettings(editable)).toEqual({ ok: true, runtime })
  })

  it("shows stored defaults as blank fields, and streaming off as off", () => {
    expect(
      runtimeFormFromSettings({
        request_timeout: 0,
        rpm: 0,
        streaming: false,
        extra_body: {},
      }),
    ).toEqual({ ...EMPTY_RUNTIME_FORM, streaming: false })
  })

  it("sends a redacted proxy back byte for byte so the server keeps the secret", () => {
    // The server shows the proxy password as ****; resending exactly that
    // value tells it to keep the stored proxy.
    const runtime = { proxy: "http://user:****@proxy.example.test:8080" }
    expect(buildRuntimeSettings(runtimeFormFromSettings(runtime))).toEqual({
      ok: true,
      runtime,
    })
  })
})
