import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { render, screen, waitFor } from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import { ToolCardSettings } from "./tool-card-settings"

vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(async () => null),
}))
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), warning: vi.fn() },
}))

const reply = (value: unknown) =>
  Promise.resolve(new Response(JSON.stringify(value), { status: 200 }))

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn(() =>
      reply({
        tools: {
          message: { enabled: true, targets: "any" },
          install_skill: { enabled: true },
        },
      }),
    ),
  )
})

afterEach(() => vi.unstubAllGlobals())

function withQueries(children: ReactNode) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>{children}</QueryClientProvider>,
  )
}

describe("tool settings on the tool cards", () => {
  it("shows where the message tool may send", async () => {
    withQueries(<ToolCardSettings toolName="message" />)
    const select = screen.getByRole("combobox", { name: "Send To" })
    await waitFor(() => expect(select).toHaveTextContent("Any chat"))
  })

  // Whether installing a skill asks first is the approval policy's call.
  it.each(["install_skill", "exec"])("adds nothing to %s", (toolName) => {
    const { container } = withQueries(<ToolCardSettings toolName={toolName} />)
    expect(container).toBeEmptyDOMElement()
  })
})
