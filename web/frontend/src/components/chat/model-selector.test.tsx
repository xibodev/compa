import { render, screen } from "@testing-library/react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"

import { ModelSelector } from "./model-selector"
import { chatSelectionValues } from "./model-selector.utils"

const targets = [
  {
    target: "first/shared",
    instance_id: "first",
    model_id: "shared",
    provider_kind: "openai",
    fetched_at: "",
  },
  {
    target: "second/shared",
    instance_id: "second",
    model_id: "shared",
    provider_kind: "openai",
    fetched_at: "",
  },
]
const routes = [{ name: "primary", targets: ["first/shared"] }]

describe("ModelSelector", () => {
  it("remains visible and says so when no default model is set", () => {
    render(
      <ModelSelector
        selection=""
        defaultSelection=""
        routes={[]}
        targets={[]}
        onValueChange={vi.fn()}
      />,
    )
    expect(
      screen.getByRole("combobox", { name: "Chat model" }),
    ).toHaveTextContent("No default model")
  })

  it("labels the default entry with the configured default selection", () => {
    render(
      <ModelSelector
        selection=""
        defaultSelection="primary"
        routes={routes}
        targets={targets}
        onValueChange={vi.fn()}
      />,
    )
    expect(screen.getByRole("combobox")).toHaveTextContent("primary (Default)")
  })

  it("stays neutral while the default model is not known", () => {
    render(
      <ModelSelector
        selection=""
        defaultSelection={null}
        routes={routes}
        targets={targets}
        onValueChange={vi.fn()}
      />,
    )
    expect(screen.getByRole("combobox")).toHaveTextContent(
      "Use configured default",
    )
  })

  it("shows the chat's own selection by name, with the exact target as its tooltip", () => {
    render(
      <ModelSelector
        selection="second/shared"
        defaultSelection="primary"
        routes={routes}
        targets={targets}
        onValueChange={vi.fn()}
      />,
    )
    const selector = screen.getByRole("combobox")
    expect(selector).toHaveTextContent("shared · second")
    expect(selector).toHaveAttribute("title", "second/shared")
  })

  it("reads models by their labels and providers by their instance labels", () => {
    render(
      <ModelSelector
        selection="ext-alpha/big/free"
        defaultSelection=""
        routes={[]}
        targets={[
          {
            target: "ext-alpha/big/free",
            instance_id: "ext-alpha",
            model_id: "big/free",
            provider_kind: "extension",
            label: "Big Model",
            instance_label: "Alpha Service",
            fetched_at: "",
          },
        ]}
        onValueChange={vi.fn()}
      />,
    )
    const selector = screen.getByRole("combobox")
    expect(selector).toHaveTextContent("Big Model · Alpha Service")
    expect(selector).not.toHaveTextContent("ext-alpha")
    expect(selector).toHaveAttribute("title", "ext-alpha/big/free")
  })

  it("derives routes and distinct exact targets", () => {
    expect(chatSelectionValues(targets, routes)).toEqual({
      routes: ["primary"],
      targets: ["first/shared", "second/shared"],
    })
    expect(JSON.stringify(chatSelectionValues(targets, routes))).not.toContain(
      "gpt-5.4-mini",
    )
  })
})
