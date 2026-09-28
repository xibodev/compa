import { describe, expect, it } from "vitest"

import { MIN_PASSWORD_LENGTH, validateSetupPassword } from "./password-rules"

describe("validateSetupPassword", () => {
  it("asks for both fields when empty", () => {
    expect(validateSetupPassword("", "")).toEqual({
      password: "required",
      confirm: "required",
    })
    expect(validateSetupPassword("   ", "")).toEqual({
      password: "required",
      confirm: "required",
    })
  })

  it("counts characters, not UTF-16 units, like the server", () => {
    expect(MIN_PASSWORD_LENGTH).toBe(8)
    expect(validateSetupPassword("1234567", "1234567").password).toBe(
      "tooShort",
    )
    // Seven emoji are fourteen UTF-16 units but seven characters.
    const emoji = "😀".repeat(7)
    expect(validateSetupPassword(emoji, emoji).password).toBe("tooShort")
    expect(validateSetupPassword("12345678", "12345678")).toEqual({})
  })

  it("compares trimmed values", () => {
    expect(validateSetupPassword(" long enough ", "long enough")).toEqual({})
    expect(validateSetupPassword("long enough", "long enougH").confirm).toBe(
      "mismatch",
    )
  })
})
