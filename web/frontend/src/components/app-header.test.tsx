import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import { TooltipProvider } from "@/components/ui/tooltip"
import "@/i18n"
import { navigateTo } from "@/lib/navigate"

import { AppHeader } from "./app-header"

vi.mock("@/lib/navigate", () => ({ navigateTo: vi.fn() }))
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="/">{children}</a>,
}))
vi.mock("@/components/ui/sidebar", () => ({ SidebarTrigger: () => null }))
vi.mock("@/components/gateway-status", () => ({
  GatewayStatusControl: () => null,
}))
vi.mock("@/hooks/use-gateway", () => ({
  useGateway: () => ({
    state: "running",
    loading: false,
    canStart: true,
    restartRequired: false,
    restart: vi.fn(),
  }),
}))

let posts: string[] = []

beforeEach(() => {
  posts = []
  vi.mocked(navigateTo).mockClear()
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === "POST") posts.push(String(input))
      return Promise.resolve(
        new Response(JSON.stringify({ status: "ok" }), { status: 200 }),
      )
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

const openSignOut = () => {
  render(
    <TooltipProvider>
      <AppHeader />
    </TooltipProvider>,
  )
  fireEvent.click(screen.getByRole("button", { name: "Sign out" }))
}

describe("signing out", () => {
  it("offers to sign every browser out beside signing this one out", async () => {
    openSignOut()
    fireEvent.click(
      await screen.findByRole("button", { name: "Sign out everywhere" }),
    )
    await waitFor(() =>
      expect(navigateTo).toHaveBeenCalledWith("/launcher-login"),
    )
    expect(posts).toEqual(["/api/auth/logout-all"])
  })

  it("still signs out only this browser", async () => {
    openSignOut()
    const dialog = await screen.findByRole("alertdialog")
    fireEvent.click(
      Array.from(dialog.querySelectorAll("button")).find(
        (button) => button.textContent === "Sign out",
      )!,
    )
    await waitFor(() =>
      expect(navigateTo).toHaveBeenCalledWith("/launcher-login"),
    )
    expect(posts).toEqual(["/api/auth/logout"])
  })
})
