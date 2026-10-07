import { fireEvent, render, screen } from "@testing-library/react"
import type { ReactNode } from "react"
import { createRef } from "react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"

import { SessionHistoryMenu } from "./session-history-menu"

// Radix menus take many seconds to open under jsdom, so the menu renders
// as plain elements here. What matters is which elements are menu items: the
// menu moves keyboard focus between items only.
vi.mock("@/components/ui/dropdown-menu", () => ({
  DropdownMenu: ({ children }: { children: ReactNode }) => (
    <div>{children}</div>
  ),
  DropdownMenuTrigger: ({ children }: { children: ReactNode }) => (
    <>{children}</>
  ),
  DropdownMenuContent: ({ children }: { children: ReactNode }) => (
    <div role="menu">{children}</div>
  ),
  DropdownMenuItem: ({
    children,
    onClick,
    onSelect,
    "aria-label": ariaLabel,
  }: {
    children: ReactNode
    onClick?: () => void
    onSelect?: () => void
    "aria-label"?: string
  }) => (
    <div
      role="menuitem"
      tabIndex={-1}
      aria-label={ariaLabel}
      onClick={() => {
        onClick?.()
        onSelect?.()
      }}
    >
      {children}
    </div>
  ),
}))

describe("SessionHistoryMenu", () => {
  it("makes deleting a chat a menu item of its own, so the keyboard reaches it", async () => {
    vi.spyOn(window, "requestAnimationFrame").mockImplementation((step) => {
      step(0)
      return 0
    })
    const onDeleteSession = vi.fn()
    render(
      <SessionHistoryMenu
        sessions={[
          {
            id: "s1",
            title: "Trip plans",
            preview: "",
            message_count: 4,
            created: "2026-01-01T00:00:00Z",
            updated: "2026-01-01T00:00:00Z",
          },
        ]}
        activeSessionId="other"
        hasMore={false}
        loadError={false}
        loadErrorMessage=""
        observerRef={createRef<HTMLDivElement>()}
        onOpenChange={() => {}}
        onSwitchSession={() => {}}
        onDeleteSession={onDeleteSession}
      />,
    )

    const deleteItem = screen.getByRole("menuitem", { name: "Delete session" })
    // Not nested in the session's item, where focus could never land on it.
    expect(deleteItem.parentElement?.closest('[role="menuitem"]')).toBeNull()
    expect(screen.getAllByRole("menuitem")).toHaveLength(2)

    fireEvent.click(deleteItem)
    expect(
      await screen.findByText(
        "Delete “Trip plans”? This permanently removes its chat history.",
      ),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Delete session" }))
    expect(onDeleteSession).toHaveBeenCalledWith("s1")
  })
})
