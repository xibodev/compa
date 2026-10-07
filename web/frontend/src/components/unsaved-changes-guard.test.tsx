import {
  Outlet,
  RouterProvider,
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
} from "@tanstack/react-router"
import { act, fireEvent, render, screen } from "@testing-library/react"
import { useState } from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import { UnsavedChangesGuard } from "./unsaved-changes-guard"

function Editor() {
  const [dirty, setDirty] = useState(false)
  return (
    <>
      <UnsavedChangesGuard when={dirty} />
      <button type="button" onClick={() => setDirty(true)}>
        edit
      </button>
    </>
  )
}

function renderPages() {
  const root = createRootRoute({ component: () => <Outlet /> })
  const editor = createRoute({
    getParentRoute: () => root,
    path: "/",
    component: Editor,
  })
  const other = createRoute({
    getParentRoute: () => root,
    path: "/other",
    component: () => <p>other page</p>,
  })
  const router = createRouter({
    routeTree: root.addChildren([editor, other]),
    history: createMemoryHistory({ initialEntries: ["/"] }),
  })
  render(<RouterProvider router={router} />)
  return router
}

describe("UnsavedChangesGuard", () => {
  beforeEach(() => {
    // The router restores scroll on navigation; jsdom has no scrolling.
    vi.spyOn(window, "scrollTo").mockImplementation(() => {})
  })

  it("lets a clean page go", async () => {
    const router = renderPages()
    await screen.findByText("edit")

    act(() => router.history.push("/other"))

    expect(await screen.findByText("other page")).toBeInTheDocument()
  })

  it("asks before leaving unsaved edits, and stays or leaves as chosen", async () => {
    const router = renderPages()
    fireEvent.click(await screen.findByText("edit"))

    act(() => router.history.push("/other"))
    expect(
      await screen.findByText("You have unsaved configuration changes"),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }))
    expect(await screen.findByText("edit")).toBeInTheDocument()
    expect(screen.queryByText("other page")).toBeNull()

    act(() => router.history.push("/other"))
    fireEvent.click(await screen.findByRole("button", { name: "Discard" }))
    expect(await screen.findByText("other page")).toBeInTheDocument()
  })
})
