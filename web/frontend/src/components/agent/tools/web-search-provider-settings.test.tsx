import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import "@/i18n"

import { WebSearchProviderSettings } from "./web-search-provider-settings"

function renderProvider(maxResults: number) {
  render(
    <WebSearchProviderSettings
      providerLabelMap={new Map([["gemini", "Gemini"]])}
      settings={{ gemini: { enabled: true, max_results: maxResults } }}
      expandedProvider="gemini"
      onToggleProviderExpand={() => {}}
      onUpdateDraft={() => {}}
    />,
  )
}

describe("web search provider settings", () => {
  it("shows the saved result count", () => {
    renderProvider(7)
    expect(screen.getByLabelText("Max Results")).toHaveValue(7)
  })

  it("shows a cleared result count as empty, not as a number it will not use", () => {
    renderProvider(0)
    expect(screen.getByLabelText("Max Results")).toHaveValue(null)
  })

  it("labels the model field", () => {
    renderProvider(5)
    expect(screen.getByLabelText("Model")).toHaveAttribute(
      "placeholder",
      "Optional model override",
    )
  })
})
