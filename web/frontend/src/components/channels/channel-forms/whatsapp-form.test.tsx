import { act, fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import { getWhatsAppLink, startWhatsAppLink } from "@/api/channels"
import "@/i18n"

import { WhatsAppForm } from "./whatsapp-form"

vi.mock("@/api/channels", () => ({
  getWhatsAppLink: vi.fn(),
  startWhatsAppLink: vi.fn(),
}))

const QR = "data:image/png;base64,AAAA"

afterEach(() => vi.useRealTimers())

describe("WhatsAppForm", () => {
  it("links the account by QR code", async () => {
    vi.useFakeTimers()
    vi.mocked(getWhatsAppLink)
      .mockResolvedValueOnce({ status: "unlinked" })
      .mockResolvedValueOnce({ status: "waiting", qr_data_uri: QR })
      .mockResolvedValueOnce({ status: "linked", phone: "15550001111" })
    vi.mocked(startWhatsAppLink).mockResolvedValue({ status: "waiting" })
    const onLinked = vi.fn()

    render(<WhatsAppForm config={{}} onChange={() => {}} onLinked={onLinked} />)
    await act(() => vi.advanceTimersByTimeAsync(0))

    fireEvent.click(screen.getByRole("button", { name: "Link WhatsApp" }))
    await act(() => vi.advanceTimersByTimeAsync(0))
    expect(startWhatsAppLink).toHaveBeenCalledOnce()

    await act(() => vi.advanceTimersByTimeAsync(2000))
    expect(screen.getByRole("img")).toHaveAttribute("src", QR)

    await act(() => vi.advanceTimersByTimeAsync(2000))
    expect(screen.getByText("Linked: +15550001111")).toBeInTheDocument()
    expect(onLinked).toHaveBeenCalledOnce()

    // Linked: polling stops.
    await act(() => vi.advanceTimersByTimeAsync(4000))
    expect(getWhatsAppLink).toHaveBeenCalledTimes(3)
  })
})
