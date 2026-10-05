import { describe, expect, it } from "vitest"

import { speakableText } from "./speakable-text"

describe("speakableText", () => {
  it("drops @artifact lines and code blocks", () => {
    const reply = [
      "Here is the clip.",
      '@artifact {"id":"a1","kind":"video"}',
      "```js",
      "const a = 1",
      "```",
      "Enjoy.",
    ].join("\n")

    expect(speakableText(reply)).toBe("Here is the clip.\nEnjoy.")
  })

  it("reads markdown as words, not symbols", () => {
    const reply = [
      "# Title",
      "Some **bold**, *italic* and `code`, see [the docs](https://x.y).",
      "- first",
      "1. second",
      "> quoted",
      "---",
      "![a chart](https://x.y/c.png)",
    ].join("\n")

    expect(speakableText(reply)).toBe(
      [
        "Title",
        "Some bold, italic and code, see the docs.",
        "first",
        "second",
        "quoted",
        "a chart",
      ].join("\n"),
    )
  })

  it("keeps comparisons and snake_case intact", () => {
    expect(speakableText("Use file_name_here when x < 5 and y > 3.")).toBe(
      "Use file_name_here when x < 5 and y > 3.",
    )
  })

  it("drops everything after a fence that never closes", () => {
    expect(speakableText("Before.\n```\nunfinished")).toBe("Before.")
  })
})
