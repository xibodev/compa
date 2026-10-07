import { describe, expect, it } from "vitest"

import {
  type ChatModelSources,
  resolveChatModelAvailability,
} from "./model-availability"

const target = {
  target: "first/model",
  instance_id: "first",
  model_id: "model",
  provider_kind: "openai",
  fetched_at: "",
}
const route = { name: "primary", targets: ["first/model"] }

const sources = (overrides: Partial<ChatModelSources>): ChatModelSources => ({
  loading: false,
  defaultSelection: "",
  defaultFailed: false,
  targets: [],
  routes: [],
  selectionsFailed: false,
  selection: "",
  ...overrides,
})

describe("resolveChatModelAvailability", () => {
  it("waits while the default or the selectable models load", () => {
    expect(
      resolveChatModelAvailability(
        sources({ loading: true, targets: [target] }),
      ),
    ).toBe("loading")
  })

  it("is unavailable only without a default and without selectable models", () => {
    expect(resolveChatModelAvailability(sources({}))).toBe("unavailable")
    expect(
      resolveChatModelAvailability(sources({ selection: "gone/model" })),
    ).toBe("unavailable")
  })

  it("is ready with a default model even when nothing is selectable", () => {
    expect(
      resolveChatModelAvailability(
        sources({ defaultSelection: "first/model" }),
      ),
    ).toBe("ready")
    expect(
      resolveChatModelAvailability(sources({ defaultSelection: "primary" })),
    ).toBe("ready")
  })

  it("leaves a chat without a default unselected when models can be picked", () => {
    expect(resolveChatModelAvailability(sources({ targets: [target] }))).toBe(
      "unselected",
    )
    expect(resolveChatModelAvailability(sources({ routes: [route] }))).toBe(
      "unselected",
    )
  })

  it("is ready with a selectable chat selection and no default", () => {
    expect(
      resolveChatModelAvailability(
        sources({ targets: [target], selection: "first/model" }),
      ),
    ).toBe("ready")
    expect(
      resolveChatModelAvailability(
        sources({ routes: [route], selection: "primary" }),
      ),
    ).toBe("ready")
  })

  it("accepts the default itself as the chat selection", () => {
    expect(
      resolveChatModelAvailability(
        sources({
          defaultSelection: "other/model",
          targets: [target],
          selection: "other/model",
        }),
      ),
    ).toBe("ready")
  })

  it("flags a chat selection that is no longer selectable", () => {
    expect(
      resolveChatModelAvailability(
        sources({ targets: [target], selection: "gone/model" }),
      ),
    ).toBe("invalid")
  })

  it("cannot check a chat selection when the selectable models failed to load", () => {
    expect(
      resolveChatModelAvailability(
        sources({
          defaultSelection: "primary",
          selectionsFailed: true,
          selection: "gone/model",
        }),
      ),
    ).toBe("ready")
  })

  it("reports a failed load when no usable model is known", () => {
    expect(
      resolveChatModelAvailability(sources({ selectionsFailed: true })),
    ).toBe("loadFailed")
    expect(resolveChatModelAvailability(sources({ defaultFailed: true }))).toBe(
      "loadFailed",
    )
  })

  it("does not claim a missing default when it could not be loaded", () => {
    expect(
      resolveChatModelAvailability(
        sources({ defaultFailed: true, targets: [target] }),
      ),
    ).toBe("ready")
  })
})
