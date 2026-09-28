import { render, screen } from "@testing-library/react"
import { describe, expect, it } from "vitest"

import { BRAND_NAME, BrandLogo } from "./brand-logo"

const mark = (container: HTMLElement) => {
  const svg = container.querySelector("svg")
  if (!svg) throw new Error("no mark rendered")
  return svg
}

describe("BrandLogo", () => {
  it("shows the product name beside a mark that screen readers skip", () => {
    const { container } = render(<BrandLogo />)
    expect(BRAND_NAME).toBe("Compa")
    // The kit's wordmark is lowercase artwork; the text stays the proper name.
    expect(screen.getByText("Compa")).toHaveClass("lowercase")
    expect(mark(container)).toHaveAttribute("aria-hidden", "true")
    expect(screen.queryByRole("img")).not.toBeInTheDocument()
  })

  it("names the mark when it stands alone", () => {
    render(<BrandLogo withName={false} />)
    expect(screen.getByRole("img", { name: "Compa" })).toBeInTheDocument()
    expect(screen.queryByText("Compa")).not.toBeInTheDocument()
  })

  it("keeps a decorative mark away from screen readers", () => {
    const { container } = render(<BrandLogo withName={false} decorative />)
    expect(screen.queryByRole("img")).not.toBeInTheDocument()
    expect(mark(container)).toHaveAttribute("aria-hidden", "true")
  })

  it("passes class names to the frame, the mark and the name", () => {
    const { container } = render(
      <BrandLogo
        className="mb-1"
        markClassName="size-10"
        nameClassName="text-2xl"
      />,
    )
    expect(container.firstElementChild).toHaveClass("mb-1")
    expect(mark(container)).toHaveClass("size-10")
    expect(mark(container)).not.toHaveClass("size-7")
    expect(screen.getByText("Compa")).toHaveClass("text-2xl")
    expect(screen.getByText("Compa")).not.toHaveClass("text-lg")
  })

  it("draws the primary mark, and the inverse mark on the dark theme", () => {
    const { container } = render(<BrandLogo withName={false} />)
    expect(mark(container)).toHaveAttribute("viewBox", "0 0 100 100")
    const [outer, inner] = mark(container).querySelectorAll("path")
    const core = mark(container).querySelector("rect")
    expect(outer).toHaveClass("fill-[#3D63D8]", "dark:fill-[#9CABFF]")
    expect(inner).toHaveClass("fill-[#2749AD]", "dark:fill-[#E8ECFF]")
    expect(inner).toHaveAttribute("opacity", "0.48")
    expect(core).toHaveClass("fill-[#2749AD]", "dark:fill-[#E8ECFF]")
  })
})
