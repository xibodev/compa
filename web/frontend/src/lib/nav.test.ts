import { describe, expect, it } from "vitest"

import { activeNavUrl } from "./nav"

const URLS = ["/", "/models", "/channels", "/config", "/config/voice", "/logs"]

describe("activeNavUrl", () => {
  it("matches Chat only on the root", () => {
    expect(activeNavUrl("/", URLS)).toBe("/")
    expect(activeNavUrl("/models", URLS)).toBe("/models")
  })

  it("keeps a channel page under the one Channels entry", () => {
    expect(activeNavUrl("/channels", URLS)).toBe("/channels")
    expect(activeNavUrl("/channels/telegram", URLS)).toBe("/channels")
  })

  it("prefers the longest entry, so Voice is not Config", () => {
    expect(activeNavUrl("/config/voice", URLS)).toBe("/config/voice")
    expect(activeNavUrl("/config/raw", URLS)).toBe("/config")
  })

  it("matches nothing for an unknown page or a mere prefix", () => {
    expect(activeNavUrl("/nowhere", URLS)).toBeUndefined()
    expect(activeNavUrl("/modelsx", URLS)).toBeUndefined()
  })
})
