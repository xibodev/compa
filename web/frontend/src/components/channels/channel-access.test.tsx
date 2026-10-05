import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import {
  effectiveDMPolicy,
  effectiveGroupPolicy,
  effectiveWhatsAppChats,
} from "./channel-config-fields"
import { GenericForm } from "./channel-forms/generic-form"
import { ChannelPairingRequests } from "./channel-pairing-requests"

const reply = (value: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(value), { status }))

describe("channel access policies", () => {
  it("shows the policy the gateway derives when none is set", () => {
    expect(effectiveDMPolicy({})).toBe("pairing")
    expect(effectiveDMPolicy({ allow_from: ["123"] })).toBe("allowlist")
    expect(effectiveDMPolicy({ allow_from: ["*"] })).toBe("open")
    expect(effectiveGroupPolicy({ allow_from: ["123"] })).toBe("allowlist")
    expect(effectiveGroupPolicy({ allow_from: [" * "] })).toBe("open")
    expect(effectiveWhatsAppChats({})).toBe("self")
  })

  it("shows a set policy over allow_from", () => {
    expect(
      effectiveDMPolicy({ allow_from: ["*"], dm_policy: "disabled" }),
    ).toBe("disabled")
    expect(effectiveGroupPolicy({ group_policy: "open" })).toBe("open")
    expect(effectiveWhatsAppChats({ chats: "all" })).toBe("all")
  })

  it("puts the policies beside Allow From of a chat channel only", () => {
    const { unmount } = render(
      <GenericForm
        config={{ enabled: true, server: "irc.example.test" }}
        onChange={() => {}}
        accessPolicy
      />,
    )
    expect(screen.getByText("Allow From")).toBeInTheDocument()
    expect(
      screen.getByRole("combobox", { name: "DM Policy" }),
    ).toHaveTextContent("Pairing")
    expect(
      screen.getByRole("combobox", { name: "Group Policy" }),
    ).toHaveTextContent("Allowlist")
    expect(
      screen.queryByRole("combobox", { name: "Chats" }),
    ).not.toBeInTheDocument()
    unmount()

    render(
      <GenericForm
        config={{ enabled: true, token: "", dm_policy: "open" }}
        onChange={() => {}}
      />,
    )
    expect(
      screen.queryByRole("combobox", { name: "DM Policy" }),
    ).not.toBeInTheDocument()
    // A policy is never shown as a raw text field.
    expect(screen.queryByText("Dm Policy")).not.toBeInTheDocument()
  })

  it("lets native WhatsApp choose its chats", () => {
    render(
      <GenericForm
        config={{ enabled: true, chats: "allowed" }}
        onChange={() => {}}
        accessPolicy
        whatsAppChats
      />,
    )
    expect(screen.getByRole("combobox", { name: "Chats" })).toHaveTextContent(
      "Allowed",
    )
  })
})

describe("ChannelPairingRequests", () => {
  afterEach(() => vi.unstubAllGlobals())

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

  const ANA = {
    sender_id: "telegram:123",
    platform_id: "123",
    display_name: "Ana",
    first_seen: "2026-10-01T10:00:00Z",
    last_seen: "2026-10-01T10:05:00Z",
    count: 2,
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
      <ChannelPairingRequests
        channelName="whatsapp_native"
        onApproved={onApproved}
      />,
    )
    fireEvent.click(await screen.findByRole("button", { name: "Deny" }))
    await waitFor(() =>
      expect(decisions[0]?.path).toBe(
        "/api/channels/whatsapp_native/pairing/deny",
      ),
    )
    expect(onApproved).not.toHaveBeenCalled()
  })
})
