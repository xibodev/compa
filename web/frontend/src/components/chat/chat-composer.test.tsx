import { render, screen } from "@testing-library/react"
import type { ComponentProps } from "react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"

import { ChatComposer, type ChatInputDisabledReason } from "./chat-composer"

const renderComposer = (props: Partial<ComponentProps<typeof ChatComposer>>) =>
  render(
    <ChatComposer
      input=""
      attachments={[]}
      onInputChange={vi.fn()}
      onAddImages={vi.fn()}
      onPaste={vi.fn()}
      onDragEnter={vi.fn()}
      onDragLeave={vi.fn()}
      onDragOver={vi.fn()}
      onDrop={vi.fn()}
      onRemoveAttachment={vi.fn()}
      onSend={vi.fn()}
      inputDisabledReason={null}
      canSend={false}
      isDragActive={false}
      onVoiceModeChange={vi.fn()}
      onToggleRecord={vi.fn()}
      onToggleAutoSpeak={vi.fn()}
      onToggleLive={vi.fn()}
      {...props}
    />,
  )

describe("ChatComposer voice controls", () => {
  it("shows no voice control when voice is not set up", () => {
    renderComposer({})
    expect(
      screen.queryByRole("button", { name: "Speak to agent" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Spoken replies" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Push-to-talk" }),
    ).not.toBeInTheDocument()
  })

  it("shows only the speaker toggle for spoken replies alone", () => {
    renderComposer({ canSpeak: true })
    expect(
      screen.getByRole("button", { name: "Spoken replies" }),
    ).toHaveAttribute("aria-pressed", "false")
    expect(
      screen.queryByRole("button", { name: "Speak to agent" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Push-to-talk" }),
    ).not.toBeInTheDocument()
  })

  it("shows only the microphone for dictation alone", () => {
    renderComposer({ canDictate: true })
    expect(
      screen.getByRole("button", { name: "Speak to agent" }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Spoken replies" }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Push-to-talk" }),
    ).not.toBeInTheDocument()
  })

  it("offers hands-free, in the UI font, only with both directions", () => {
    renderComposer({ canDictate: true, canSpeak: true })
    const chip = screen.getByRole("button", { name: "Push-to-talk" })
    expect(chip).not.toHaveClass("font-mono")
    expect(
      screen.getByRole("button", { name: "Speak to agent" }),
    ).toBeInTheDocument()
  })

  it("says the same about a missing model as the empty chat does", () => {
    renderComposer({ inputDisabledReason: "noModelAvailable" })
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute(
      "placeholder",
      expect.stringMatching(/^Set up a model on the Models page/),
    )
  })

  it("invites a message with a real ellipsis", () => {
    renderComposer({})
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute(
      "placeholder",
      "Start a new message…",
    )
  })

  it("only says it is connecting while the gateway is still being checked", () => {
    renderComposer({ inputDisabledReason: "gatewayUnknown" })
    const message = screen.getByRole("textbox", { name: "Message" })
    expect(message).toHaveAttribute("placeholder", "Connecting…")
    expect(message).toBeDisabled()
  })

  it.each<[ChatInputDisabledReason, string]>([
    ["gatewayStarting", "Compa is starting. Chat opens in a moment."],
    ["gatewayRestarting", "Compa is restarting. Chat opens in a moment."],
    [
      "gatewayStopped",
      "Chat is paused: the gateway is stopped. Start it from the status menu at the top.",
    ],
    [
      "gatewayError",
      "Chat is unavailable: the gateway stopped with an error. See Logs, then restart it from the status menu.",
    ],
    ["websocketConnecting", "Connecting…"],
    [
      "websocketDisconnected",
      "Chat lost its connection. Reload the page if it doesn't come back.",
    ],
    [
      "websocketError",
      "Chat couldn't connect. Check that Compa is running, then reload the page.",
    ],
  ])("says plainly why it can't send: %s", (reason, text) => {
    renderComposer({ inputDisabledReason: reason })
    expect(screen.getByRole("textbox", { name: "Message" })).toHaveAttribute(
      "placeholder",
      text,
    )
  })

  it("never names the launcher or the socket when chat is unavailable", () => {
    const reasons: ChatInputDisabledReason[] = [
      "gatewayUnknown",
      "gatewayStarting",
      "gatewayRestarting",
      "gatewayStopping",
      "gatewayStopped",
      "gatewayError",
      "websocketConnecting",
      "websocketDisconnected",
      "websocketError",
      "modelLoading",
      "modelLoadError",
      "invalidSelection",
      "noModelAvailable",
    ]
    for (const reason of reasons) {
      const { unmount } = renderComposer({ inputDisabledReason: reason })
      const placeholder =
        screen
          .getByRole("textbox", { name: "Message" })
          .getAttribute("placeholder") ?? ""
      expect(placeholder).not.toMatch(/launcher|websocket|\.\.\./i)
      unmount()
    }
  })

  it("names the context meter and says what it measures", () => {
    renderComposer({
      contextUsage: {
        used_tokens: 1200,
        total_tokens: 16000,
        compress_at_tokens: 16000,
        used_percent: 7,
      },
    })
    expect(
      screen.getByRole("button", {
        name: "Context used: 7% (1.2k of 16.0k tokens)",
      }),
    ).toBeInTheDocument()
  })
})
