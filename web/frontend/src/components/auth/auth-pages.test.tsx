import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { ComponentType } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import "@/i18n"
import { navigateTo } from "@/lib/navigate"
import { Route as LoginRoute } from "@/routes/launcher-login"
import { Route as SetupRoute } from "@/routes/launcher-setup"

vi.mock("@/lib/navigate", () => ({ navigateTo: vi.fn() }))

const SetupPage = SetupRoute.options.component as ComponentType
const LoginPage = LoginRoute.options.component as ComponentType

const reply = (value: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(value), { status }))

let calls: { path: string; body?: unknown }[] = []
let setupStatus = 200

beforeEach(() => {
  calls = []
  setupStatus = 200
  vi.mocked(navigateTo).mockClear()
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      calls.push({
        path,
        body: init?.body ? JSON.parse(String(init.body)) : undefined,
      })
      if (path === "/api/auth/status")
        return reply({ authenticated: false, initialized: true })
      if (path === "/api/auth/setup" && setupStatus !== 200)
        return reply({ error: "refused" }, setupStatus)
      return reply({ status: "ok" })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  window.history.replaceState({}, "", "/")
})

const posts = (path: string) => calls.filter((call) => call.path === path)

const submitSetup = (password: string) => {
  fireEvent.change(screen.getByLabelText("Password"), {
    target: { value: password },
  })
  fireEvent.change(screen.getByLabelText("Confirm password"), {
    target: { value: password },
  })
  fireEvent.click(screen.getByRole("button", { name: "Set password" }))
}

describe("setting the password", () => {
  it("shows the product and validates every field inline, the same way", async () => {
    render(<SetupPage />)
    expect(screen.getByText("Compa")).toBeInTheDocument()
    expect(
      screen.getByRole("heading", { name: "Set a password for Compa" }),
    ).toBeInTheDocument()
    const form = screen
      .getByRole("button", { name: "Set password" })
      .closest("form")
    expect(form).toHaveAttribute("novalidate")

    fireEvent.click(screen.getByRole("button", { name: "Set password" }))
    expect(await screen.findByText("Enter a password.")).toBeInTheDocument()
    expect(
      screen.getByText("Repeat the password to confirm it."),
    ).toBeInTheDocument()
    expect(screen.getByLabelText("Password")).toHaveAttribute(
      "aria-invalid",
      "true",
    )

    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "short" },
    })
    expect(
      await screen.findByText("Use at least 8 characters."),
    ).toBeInTheDocument()

    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "long enough" },
    })
    fireEvent.change(screen.getByLabelText("Confirm password"), {
      target: { value: "different" },
    })
    expect(
      await screen.findByText("Passwords do not match."),
    ).toBeInTheDocument()
    expect(posts("/api/auth/setup")).toHaveLength(0)
  })

  it("signs in with the new password and opens the app", async () => {
    window.history.replaceState({}, "", "/launcher-setup?token=tok-123")
    render(<SetupPage />)
    submitSetup("correct horse")

    await waitFor(() => expect(navigateTo).toHaveBeenCalledWith("/"))
    // The token of the link the launcher opened authorizes the first password.
    expect(posts("/api/auth/setup")[0].body).toEqual({
      password: "correct horse",
      confirm: "correct horse",
      setup_token: "tok-123",
    })
    expect(posts("/api/auth/login")[0].body).toEqual({
      password: "correct horse",
    })
  })

  it.each([
    [
      403,
      "This setup link is not valid. Use the link Compa opened or printed when it started.",
    ],
    [409, "A password is already set. Sign in instead."],
  ])("says why a %i refused the password", async (status, message) => {
    setupStatus = status
    render(<SetupPage />)
    submitSetup("correct horse")

    expect(await screen.findByRole("alert")).toHaveTextContent(message)
    expect(posts("/api/auth/setup")[0].body).not.toHaveProperty("setup_token")
    expect(posts("/api/auth/login")).toHaveLength(0)
    expect(navigateTo).not.toHaveBeenCalled()
  })
})

describe("signing in", () => {
  it("shows the product and asks for the password inline", async () => {
    render(<LoginPage />)
    expect(screen.getByText("Compa")).toBeInTheDocument()
    expect(
      screen.getByText("Enter your password to open Compa."),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(await screen.findByText("Enter your password.")).toBeInTheDocument()
    expect(posts("/api/auth/login")).toHaveLength(0)
  })
})
