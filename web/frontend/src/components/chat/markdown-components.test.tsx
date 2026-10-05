import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import ReactMarkdown from "react-markdown"
import { afterEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import {
  MARKDOWN_COMPONENTS,
  MARKDOWN_REHYPE_PLUGINS,
  MARKDOWN_REMARK_PLUGINS,
} from "./markdown"

function renderMarkdown(markdown: string) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={client}>
      <ReactMarkdown
        remarkPlugins={MARKDOWN_REMARK_PLUGINS}
        rehypePlugins={MARKDOWN_REHYPE_PLUGINS}
        components={MARKDOWN_COMPONENTS}
      >
        {markdown}
      </ReactMarkdown>
    </QueryClientProvider>,
  )
}

const launcherConfig = (extra: Record<string, unknown> = {}) =>
  vi.fn(() =>
    Promise.resolve(
      new Response(
        JSON.stringify({
          port: 18800,
          public: false,
          allowed_cidrs: [],
          allow_localhost_bypass: true,
          trusted_proxy_cidrs: [],
          ...extra,
        }),
        { status: 200 },
      ),
    ),
  )

describe("reply markdown", () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it("loads an image from another site only when clicked", async () => {
    vi.stubGlobal("fetch", launcherConfig())
    renderMarkdown("![a chart](https://tracker.example/pixel.png)")

    expect(document.querySelector("img")).toBeNull()
    fireEvent.click(screen.getByRole("button", { name: "a chart" }))

    const image = await screen.findByRole("img", { name: "a chart" })
    expect(image).toHaveAttribute("src", "https://tracker.example/pixel.png")
    expect(image).toHaveAttribute("referrerpolicy", "no-referrer")
  })

  it("names an image without alt text by its site", () => {
    vi.stubGlobal("fetch", launcherConfig())
    renderMarkdown("![](https://cdn.example/a.png)")

    expect(
      screen.getByRole("button", { name: "cdn.example" }),
    ).toBeInTheDocument()
  })

  it("loads remote images at once when the launcher says always", async () => {
    vi.stubGlobal("fetch", launcherConfig({ remote_images: "always" }))
    renderMarkdown("![a chart](https://cdn.example/chart.png)")

    await waitFor(() =>
      expect(screen.getByRole("img", { name: "a chart" })).toBeInTheDocument(),
    )
  })

  it("shows an image the dashboard serves itself", () => {
    vi.stubGlobal("fetch", launcherConfig())
    renderMarkdown("![own](/web/media/abc)")

    expect(screen.getByRole("img", { name: "own" })).toHaveAttribute(
      "src",
      "/web/media/abc",
    )
  })

  it("opens links in a new tab without handing over the page", () => {
    vi.stubGlobal("fetch", launcherConfig())
    renderMarkdown("[docs](https://example.com/docs)")

    const link = screen.getByRole("link", { name: "docs" })
    expect(link).toHaveAttribute("target", "_blank")
    expect(link).toHaveAttribute("rel", "noopener noreferrer")
  })
})
