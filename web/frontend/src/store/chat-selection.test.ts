import { afterEach, describe, expect, it } from "vitest"

import { getChatSelection, setChatSelection, updateChatStore } from "./chat"

describe("transient chat selection", () => {
  afterEach(() => updateChatStore({ selectionBySession: {} }))

  it("isolates selection by session and clears to configured default", () => {
    setChatSelection("one", " first/model ")
    setChatSelection("two", "primary")
    expect(getChatSelection("one")).toBe("first/model")
    expect(getChatSelection("two")).toBe("primary")
    setChatSelection("one", "")
    expect(getChatSelection("one")).toBe("")
    expect(getChatSelection("two")).toBe("primary")
  })
})
