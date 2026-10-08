import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import type { ReactNode } from "react"
import { afterEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import { hasOwnerAccount } from "./channel-config-fields"
import { ChannelConfigPage } from "./channel-config-page"
import { GenericForm } from "./channel-forms/generic-form"
import { ChannelPairingRequests } from "./channel-pairing-requests"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="/">{children}</a>,
  useBlocker: () => ({ status: "idle" }),
}))
vi.mock("@/components/ui/sidebar", () => ({ SidebarTrigger: () => null }))
vi.mock("@/hooks/use-gateway", () => ({
  useGateway: () => ({ state: "running" }),
}))
vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(async () => null),
}))

const reply = (value: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(value), { status }))

const ANA = {
  sender_id: "telegram:123",
  platform_id: "123",
  display_name: "Ana",
  first_seen: "2026-10-01T10:00:00Z",
  last_seen: "2026-10-01T10:05:00Z",
  count: 2,
}

afterEach(() => vi.unstubAllGlobals())

describe("channel access", () => {
  it("shows Allow From of a chat channel even while empty", () => {
    const { unmount } = render(
      <GenericForm
        config={{ enabled: true, server: "irc.example.test" }}
        onChange={() => {}}
        accessPolicy
      />,
    )
    expect(screen.getByText("Allow From")).toBeInTheDocument()
    // Who may talk to the channel is not a choice.
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
    unmount()

    render(
      <GenericForm config={{ enabled: true, token: "" }} onChange={() => {}} />,
    )
    expect(screen.queryByText("Allow From")).not.toBeInTheDocument()
  })

  it("takes an allow_from entry other than * as the owner's account", () => {
    expect(hasOwnerAccount({})).toBe(false)
    expect(hasOwnerAccount({ allow_from: [] })).toBe(false)
    expect(hasOwnerAccount({ allow_from: ["*", " * ", " "] })).toBe(false)
    expect(hasOwnerAccount({ allow_from: [" @ana "] })).toBe(true)
    expect(hasOwnerAccount({ allow_from: ["*", "telegram:123"] })).toBe(true)
  })
})

describe("ChannelPairingRequests", () => {
  function stubPairing(initial: unknown[]) {
    let requests = initial
    const decisions: { path: string; body: unknown }[] = []
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input)
        if (init?.method === "POST") {
          const body = JSON.parse(String(init.body)) as { sender_id: string }
          decisions.push({ path, body })
          requests = requests.filter(
            (r) => (r as { sender_id: string }).sender_id !== body.sender_id,
          )
          return reply({ status: "ok" })
        }
        return reply({ requests })
      }),
    )
    return decisions
  }

  it("shows nothing while no sender waits", async () => {
    stubPairing([])
    const { container } = render(
      <ChannelPairingRequests channelName="telegram" onApproved={() => {}} />,
    )
    await waitFor(() => expect(fetch).toHaveBeenCalled())
    expect(container).toBeEmptyDOMElement()
  })

  it("approves a waiting sender and refreshes the list", async () => {
    const decisions = stubPairing([ANA])
    const onApproved = vi.fn()
    render(
      <ChannelPairingRequests channelName="telegram" onApproved={onApproved} />,
    )
    const row = (await screen.findByText("Ana")).closest("li")!
    expect(within(row).getByText("telegram:123")).toBeInTheDocument()

    fireEvent.click(within(row).getByRole("button", { name: "Approve" }))
    await waitFor(() => expect(onApproved).toHaveBeenCalledWith("telegram:123"))
    expect(decisions).toEqual([
      {
        path: "/api/channels/telegram/pairing/approve",
        body: { sender_id: "telegram:123" },
      },
    ])
    await waitFor(() =>
      expect(screen.queryByText("Ana")).not.toBeInTheDocument(),
    )
  })

  it("denies a waiting sender", async () => {
    const decisions = stubPairing([ANA])
    const onApproved = vi.fn()
    render(
      <ChannelPairingRequests channelName="discord" onApproved={onApproved} />,
    )
    fireEvent.click(await screen.findByRole("button", { name: "Deny" }))
    await waitFor(() =>
      expect(decisions[0]?.path).toBe("/api/channels/discord/pairing/deny"),
    )
    expect(onApproved).not.toHaveBeenCalled()
  })
})

describe("pairing on the channel page", () => {
  // Serves the Telegram channel with allowFrom saved, and its pairing
  // requests; returns the paths fetched.
  function stubChannel(allowFrom: string[], initial: unknown[]) {
    let requests = initial
    const paths: string[] = []
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
        const path = String(input)
        paths.push(path)
        switch (path) {
          case "/api/channels/catalog":
            return reply({
              channels: [{ name: "telegram", config_key: "telegram" }],
            })
          case "/api/channels/telegram/config":
            return reply({
              config: { enabled: true, allow_from: allowFrom },
              configured_secrets: ["token"],
              config_key: "telegram",
            })
          case "/api/channels/telegram/pairing":
            return reply({ requests })
          case "/api/channels/telegram/pairing/approve": {
            const body = JSON.parse(String(init?.body)) as {
              sender_id: string
            }
            requests = requests.filter(
              (r) => (r as { sender_id: string }).sender_id !== body.sender_id,
            )
            return reply({ status: "ok" })
          }
          default:
            return reply({ error: "not found" }, 404)
        }
      }),
    )
    return paths
  }

  it("lists pairing requests while Allow From lists no account", async () => {
    // "*" admits no one, so it is no account.
    stubChannel(["*"], [ANA])
    render(<ChannelConfigPage channelName="telegram" />)

    const row = (await screen.findByText("Ana")).closest("li")!
    expect(screen.getByText("Pairing Requests")).toBeInTheDocument()

    fireEvent.click(within(row).getByRole("button", { name: "Approve" }))
    // The approved sender is the owner's account in Allow From, saved by
    // the approval, so the requests hide and nothing is left to save.
    expect(
      await screen.findByRole("button", { name: "Remove telegram:123" }),
    ).toBeInTheDocument()
    expect(screen.queryByText("Pairing Requests")).not.toBeInTheDocument()
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled()
  })

  it("lists no pairing requests once Allow From lists an account", async () => {
    const paths = stubChannel(["telegram:123"], [ANA])
    render(<ChannelConfigPage channelName="telegram" />)

    const remove = await screen.findByRole("button", {
      name: "Remove telegram:123",
    })
    expect(screen.queryByText("Pairing Requests")).not.toBeInTheDocument()

    // Emptying Allow From reopens pairing only once it is saved: until then
    // the gateway answers the account and records no requests.
    fireEvent.click(remove)
    expect(screen.getByRole("button", { name: "Save" })).toBeEnabled()
    expect(paths).not.toContain("/api/channels/telegram/pairing")
  })
})
