import { fireEvent, render, screen } from "@testing-library/react"
import { afterEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import { ArtifactCard } from "./artifact-card"
import { extractArtifacts, isArtifact } from "./artifact-lines"

const valid = {
  id: "clip-1",
  kind: "video",
  path: "out/clip.mp4",
  root: "workspace",
  media_type: "video/mp4",
  primitive: "video",
  bytes: 2048,
  digest: "sha256:abc",
  title: "Clip",
  module: "video.maker",
}

describe("@artifact lines", () => {
  it("draws a card from a well-formed line", () => {
    const { text, artifacts } = extractArtifacts(
      `Done.\n@artifact ${JSON.stringify(valid)}`,
    )
    expect(text).toBe("Done.")
    expect(artifacts).toEqual([valid])
  })

  it.each([
    ["null", "null"],
    ["an array", "[]"],
    ["an object as title", JSON.stringify({ ...valid, title: { x: 1 } })],
    ["bytes as text", JSON.stringify({ ...valid, bytes: "2048" })],
    ["no module", JSON.stringify({ ...valid, module: undefined })],
  ])("keeps a line holding %s as text instead of drawing a card", (_, json) => {
    const line = `@artifact ${json}`
    const { text, artifacts } = extractArtifacts(`Done.\n${line}`)
    expect(artifacts).toEqual([])
    expect(text).toBe(`Done.\n${line}`)
  })

  it("accepts a missing or null optional field", () => {
    expect(isArtifact({ ...valid, title: undefined, primitive: null })).toBe(
      true,
    )
  })
})

describe("ArtifactCard", () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("opens a PDF in a new tab instead of embedding it", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response(null, { status: 200 }))),
    )
    render(
      <ArtifactCard
        artifact={{
          ...valid,
          kind: "document",
          path: "out/report.pdf",
          media_type: "application/pdf",
          primitive: "document",
        }}
      />,
    )

    const link = await screen.findByRole("link", { name: "Open document" })
    expect(link).toHaveAttribute("target", "_blank")
    expect(link).toHaveAttribute("rel", "noopener noreferrer")
    expect(document.querySelector("object")).toBeNull()
  })

  it("does not show an error page as the artefact's contents", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((_input: RequestInfo | URL, init?: RequestInit) =>
        Promise.resolve(
          init?.method === "HEAD"
            ? new Response(null, { status: 200 })
            : new Response("<html>gateway error</html>", { status: 502 }),
        ),
      ),
    )
    render(
      <ArtifactCard
        artifact={{ ...valid, primitive: "text", media_type: "text/plain" }}
      />,
    )

    fireEvent.click(
      await screen.findByRole("button", { name: "Show contents" }),
    )

    expect(
      await screen.findByText("could not read artefact: 502"),
    ).toBeInTheDocument()
    expect(screen.queryByText(/gateway error/)).toBeNull()
  })
})
