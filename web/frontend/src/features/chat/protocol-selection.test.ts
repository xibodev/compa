import { afterEach, describe, expect, it } from "vitest"

import { getChatState, updateChatStore } from "@/store/chat"

import { handleWebChatMessage } from "./protocol"

describe("Web chat served selection metadata", () => {
  afterEach(() => updateChatStore({ messages: [] }))

  it("prefers actual served target and retains stable identity", () => {
    handleWebChatMessage(
      {
        type: "message.create",
        payload: {
          message_id: "m1",
          content: "answer",
          model_name: "requested-label",
          served_target: "second/shared",
          served_identity:
            "provider_instance:second|instance_target:second/shared",
        },
      },
      "session",
    )
    const message = getChatState().messages[0]
    expect(message.modelName).toBe("second/shared")
    expect(message.servedTarget).toBe("second/shared")
    expect(message.servedIdentity).toContain("provider_instance:second")
  })
})
