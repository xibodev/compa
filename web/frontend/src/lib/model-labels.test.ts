import { describe, expect, it } from "vitest"

import type { ProviderTarget } from "@/api/provider-instances"

import { formatModelLabel, selectionLabel } from "./model-labels"

const targets: ProviderTarget[] = [
  {
    target: "ext-alpha/big/free",
    instance_id: "ext-alpha",
    model_id: "big/free",
    provider_kind: "extension",
    label: "Big Model",
    instance_label: "Alpha Service",
    fetched_at: "",
  },
  {
    target: "plain/model-1",
    instance_id: "plain",
    model_id: "model-1",
    provider_kind: "openai",
    display_name: "Model One",
    fetched_at: "",
  },
]

describe("selectionLabel", () => {
  it("reads a known target by its model and instance labels", () => {
    const label = selectionLabel("ext-alpha/big/free", targets)
    expect(label).toEqual({
      model: "Big Model",
      provider: "Alpha Service",
      target: "ext-alpha/big/free",
    })
    expect(formatModelLabel(label)).toBe("Big Model · Alpha Service")
  })

  it("falls back to the catalog's display name and the instance id", () => {
    expect(formatModelLabel(selectionLabel("plain/model-1", targets))).toBe(
      "Model One · plain",
    )
  })

  it("splits an unknown target at its first slash only", () => {
    expect(selectionLabel("gone/vendor/model", targets)).toEqual({
      model: "vendor/model",
      provider: "gone",
      target: "gone/vendor/model",
    })
  })

  it("reads a route name as itself", () => {
    const label = selectionLabel("primary", targets)
    expect(label.provider).toBe("")
    expect(formatModelLabel(label)).toBe("primary")
  })
})
