import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"

import { Field, KeyInput } from "./shared-form"
import { Input } from "./ui/input"

describe("form fields", () => {
  it("names its control with the label without an explicit id", () => {
    render(
      <Field label="Max Tokens" hint="Upper limit." layout="setting-row">
        <Input value="1" onChange={() => {}} />
      </Field>,
    )

    expect(screen.getByLabelText("Max Tokens")).toHaveValue("1")
  })

  it("names a secret input, and its show button is reachable and named", () => {
    const onChange = vi.fn()
    render(
      <Field label="Bot Token">
        <KeyInput value="secret" onChange={onChange} />
      </Field>,
    )

    const input = screen.getByLabelText("Bot Token")
    expect(input).toHaveAttribute("type", "password")

    const toggle = screen.getByRole("button", { name: "Show secret" })
    expect(toggle).not.toHaveAttribute("tabindex", "-1")
    expect(toggle).toHaveAttribute("aria-pressed", "false")

    fireEvent.click(toggle)
    expect(input).toHaveAttribute("type", "text")
    expect(toggle).toHaveAttribute("aria-pressed", "true")
  })
})
