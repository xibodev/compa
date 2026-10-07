import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import "@/i18n"

import { UserMessage } from "./user-message"

describe("UserMessage", () => {
  it("keeps the copy button beside the bubble, not over its text", () => {
    render(<UserMessage content="A message long enough to reach the edge" />)
    const text = screen.getByText("A message long enough to reach the edge")
    const copy = screen.getByRole("button", { name: "Copy message" })
    expect(text.contains(copy)).toBe(false)
    expect(copy.className).not.toMatch(/\babsolute\b/)
  })
})
