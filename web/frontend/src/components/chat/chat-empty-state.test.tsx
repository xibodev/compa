import { render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"
import type { GatewayState } from "@/store/gateway"

import { ChatEmptyState } from "./chat-empty-state"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ to, children }: { to: string; children: ReactNode }) => (
    <a href={to}>{children}</a>
  ),
}))

describe("ChatEmptyState", () => {
  it("points to Models, naming every way to get a model", () => {
    render(
      <ChatEmptyState modelAvailability="unavailable" gatewayState="running" />,
    )
    expect(screen.getByText("No models yet")).toBeInTheDocument()
    const description = screen.getByText(/Set up a model on the Models page/)
    // Not only an API key: free providers and the extension count too.
    expect(description).toHaveTextContent(/free providers/)
    expect(description).toHaveTextContent(/extension/)
    expect(screen.getByRole("link", { name: "Set up models" })).toHaveAttribute(
      "href",
      "/models",
    )
  })

  it("asks for a model when models exist but no default is set", () => {
    render(
      <ChatEmptyState modelAvailability="unselected" gatewayState="running" />,
    )
    expect(screen.getByText("Choose a model")).toBeInTheDocument()
    expect(screen.getByRole("link", { name: "Go to Models" })).toBeVisible()
  })

  it("welcomes a ready chat and reports a stopped gateway", () => {
    const { rerender } = render(
      <ChatEmptyState modelAvailability="ready" gatewayState="running" />,
    )
    expect(screen.getByText("What would you like to make?")).toBeVisible()
    // Modules are only mentioned when there is a Module menu to point at.
    expect(screen.queryByText(/Module menu/)).not.toBeInTheDocument()
    rerender(
      <ChatEmptyState
        modelAvailability="ready"
        gatewayState="running"
        hasModules
      />,
    )
    expect(screen.getByText(/Module menu/)).toBeVisible()
    rerender(
      <ChatEmptyState modelAvailability="ready" gatewayState="stopped" />,
    )
    expect(screen.getByText("The gateway isn't running")).toBeVisible()
    expect(screen.queryByText(/Start Gateway/)).not.toBeInTheDocument()
  })

  it.each<GatewayState>(["unknown", "starting", "restarting"])(
    "welcomes as usual while the gateway is %s",
    (gatewayState) => {
      render(
        <ChatEmptyState
          modelAvailability="loading"
          gatewayState={gatewayState}
        />,
      )
      expect(screen.getByText("What would you like to make?")).toBeVisible()
      expect(
        screen.queryByText("The gateway isn't running"),
      ).not.toBeInTheDocument()
    },
  )

  it.each<GatewayState>(["stopped", "stopping", "error"])(
    "says the gateway isn't running while it is %s",
    (gatewayState) => {
      render(
        <ChatEmptyState
          modelAvailability="ready"
          gatewayState={gatewayState}
        />,
      )
      expect(screen.getByText("The gateway isn't running")).toBeVisible()
    },
  )

  it("does not claim missing models while they load", () => {
    render(
      <ChatEmptyState modelAvailability="loading" gatewayState="running" />,
    )
    expect(screen.queryByText("No models yet")).not.toBeInTheDocument()
    expect(screen.queryByText("Choose a model")).not.toBeInTheDocument()
  })
})
