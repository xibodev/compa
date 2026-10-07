import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import {
  type ReactElement,
  type ReactNode,
  cloneElement,
  createContext,
  useContext,
  useState,
} from "react"
import { beforeEach, describe, expect, it, vi } from "vitest"

import { TooltipProvider } from "@/components/ui/tooltip"
import { useGateway } from "@/hooks/use-gateway"
import "@/i18n"

import { GatewayStatusControl } from "./gateway-status"

vi.mock("@/hooks/use-gateway", () => ({ useGateway: vi.fn() }))

// Radix positions its menu with a popper that keeps jsdom busy for many
// seconds, so the menu is replaced by one that just shows its items.
vi.mock("@/components/ui/dropdown-menu", () => {
  const Open = createContext<[boolean, (open: boolean) => void]>([
    false,
    () => {},
  ])
  const Pass = ({ children }: { children?: ReactNode }) => <>{children}</>
  return {
    DropdownMenu: function DropdownMenu({ children }: { children: ReactNode }) {
      const state = useState(false)
      return <Open.Provider value={state}>{children}</Open.Provider>
    },
    DropdownMenuTrigger: function DropdownMenuTrigger({
      children,
      asChild,
      ...props
    }: {
      children: ReactElement<{ onClick?: () => void }>
      asChild?: boolean
      onClick?: () => void
    }) {
      const [open, setOpen] = useContext(Open)
      // The child is the trigger already; asChild is not passed on.
      void asChild
      return cloneElement(children, {
        ...props,
        onClick: () => {
          props.onClick?.()
          setOpen(!open)
        },
      })
    },
    DropdownMenuContent: function DropdownMenuContent({
      children,
    }: {
      children: ReactNode
    }) {
      const [open] = useContext(Open)
      return open ? <div role="menu">{children}</div> : null
    },
    DropdownMenuItem: function DropdownMenuItem({
      children,
      disabled,
      onSelect,
    }: {
      children: ReactNode
      disabled?: boolean
      onSelect?: () => void
    }) {
      const [, setOpen] = useContext(Open)
      return (
        <button
          type="button"
          role="menuitem"
          disabled={disabled}
          onClick={() => {
            onSelect?.()
            setOpen(false)
          }}
        >
          {children}
        </button>
      )
    },
    DropdownMenuLabel: Pass,
    DropdownMenuSeparator: () => null,
  }
})

const start = vi.fn()
const stop = vi.fn()
const restart = vi.fn()

const gateway = (state: ReturnType<typeof useGateway>["state"]) =>
  vi.mocked(useGateway).mockReturnValue({
    state,
    loading: false,
    canStart: true,
    startReason: undefined,
    restartRequired: false,
    start,
    stop,
    restart,
    error: null,
  })

const renderControl = () =>
  render(
    <TooltipProvider>
      <GatewayStatusControl />
    </TooltipProvider>,
  )

const openMenu = (name: string) =>
  fireEvent.click(screen.getByRole("button", { name }))

describe("GatewayStatusControl", () => {
  beforeEach(() => vi.clearAllMocks())

  it("labels a running gateway plainly, without a red stop button", () => {
    gateway("running")
    renderControl()
    const control = screen.getByRole("button", { name: "Gateway: Running" })
    expect(control).toHaveTextContent("Running")
    expect(control).toHaveAttribute("data-variant", "ghost")
    expect(
      screen.queryByRole("button", { name: /Stop/ }),
    ).not.toBeInTheDocument()
  })

  it("asks before stopping", async () => {
    gateway("running")
    renderControl()
    openMenu("Gateway: Running")
    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Stop gateway…" }),
    )
    const dialog = await screen.findByRole("alertdialog", {
      name: "Stop the gateway?",
    })
    expect(stop).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole("button", { name: "Stop gateway" }))
    await waitFor(() => expect(stop).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(dialog).not.toBeInTheDocument())
  })

  it("offers to start a stopped gateway", async () => {
    gateway("stopped")
    renderControl()
    openMenu("Gateway: Stopped")
    fireEvent.click(
      await screen.findByRole("menuitem", { name: "Start gateway" }),
    )
    expect(start).toHaveBeenCalledTimes(1)
  })

  it("shows a starting gateway as busy", () => {
    gateway("starting")
    renderControl()
    expect(
      screen.getByRole("button", { name: "Gateway: Starting…" }),
    ).toBeInTheDocument()
  })
})
