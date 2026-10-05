import { fireEvent, render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"

import { MqttForm } from "./mqtt-form"

describe("MqttForm", () => {
  it("switches TLS verification off and on", () => {
    const onChange = vi.fn()
    const { rerender } = render(
      <MqttForm
        config={{ broker: "mqtts://broker.example.test:8883" }}
        onChange={onChange}
        configuredSecrets={[]}
      />,
    )

    const skip = screen.getByRole("switch", { name: "Skip TLS Verification" })
    expect(skip).toHaveAttribute("aria-checked", "false")
    fireEvent.click(skip)
    expect(onChange).toHaveBeenCalledWith("tls_insecure_skip_verify", true)

    rerender(
      <MqttForm
        config={{ tls_insecure_skip_verify: true }}
        onChange={onChange}
        configuredSecrets={[]}
      />,
    )
    expect(
      screen.getByRole("switch", { name: "Skip TLS Verification" }),
    ).toHaveAttribute("aria-checked", "true")
  })
})
