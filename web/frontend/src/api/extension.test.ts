import { describe, expect, it } from "vitest"

import { extensionProviderState } from "./extension"

describe("extensionProviderState", () => {
  it("gives each provider one state", () => {
    const state = (
      credential: "none" | "token" | "oauth",
      connected: boolean,
      supported = true,
    ) => extensionProviderState({ credential, connected, supported })
    expect(state("none", true)).toBe("ready")
    expect(state("none", false)).toBe("not_ready")
    expect(state("token", false)).toBe("needs_token")
    expect(state("token", true)).toBe("connected")
    expect(state("oauth", false)).toBe("needs_sign_in")
    expect(state("oauth", true)).toBe("connected")
    expect(state("oauth", true, false)).toBe("unsupported")
  })
})
