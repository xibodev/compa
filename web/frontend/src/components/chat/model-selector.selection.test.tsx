import { render, screen } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { type ReactNode, createContext, useContext } from "react"
import { describe, expect, it, vi } from "vitest"

import "@/i18n"

import { ModelSelector } from "./model-selector"

// Radix's popper-positioned Select takes many seconds to open under jsdom.
// This shim keeps every item rendered so the selector's own item values and
// its mapping of the default entry can be exercised quickly.
vi.mock("@/components/ui/select", () => {
  const Pick = createContext<(value: string) => void>(() => {})
  const Pass = ({ children }: { children?: ReactNode }) => <>{children}</>
  return {
    Select: ({
      onValueChange,
      children,
    }: {
      onValueChange: (value: string) => void
      children: ReactNode
    }) => <Pick.Provider value={onValueChange}>{children}</Pick.Provider>,
    SelectTrigger: Pass,
    SelectContent: Pass,
    SelectGroup: Pass,
    SelectLabel: Pass,
    SelectSeparator: () => null,
    SelectItem: function SelectItem({
      value,
      children,
    }: {
      value: string
      children: ReactNode
    }) {
      const pick = useContext(Pick)
      return (
        <button type="button" role="option" onClick={() => pick(value)}>
          {children}
        </button>
      )
    },
  }
})

const target = (instanceID: string) => ({
  target: `${instanceID}/shared`,
  instance_id: instanceID,
  model_id: "shared",
  provider_kind: "openai",
  fetched_at: "",
})

describe("ModelSelector selection", () => {
  it("selects routes and exact targets, and the default entry clears the selection", async () => {
    const onValueChange = vi.fn()
    const user = userEvent.setup()
    render(
      <ModelSelector
        selection="second/shared"
        defaultSelection="primary"
        routes={[{ name: "primary", targets: ["first/shared"] }]}
        targets={[target("first"), target("second")]}
        onValueChange={onValueChange}
      />,
    )

    const shared = screen.getAllByRole("option", { name: "shared" })
    expect(shared).toHaveLength(2)
    await user.click(shared[1])
    expect(onValueChange).toHaveBeenLastCalledWith("second/shared")

    await user.click(screen.getByRole("option", { name: "primary" }))
    expect(onValueChange).toHaveBeenLastCalledWith("primary")

    await user.click(screen.getByRole("option", { name: "primary (Default)" }))
    expect(onValueChange).toHaveBeenLastCalledWith("")
  })

  it("offers the no-default entry, which also leaves the selection empty", async () => {
    const onValueChange = vi.fn()
    render(
      <ModelSelector
        selection="first/shared"
        defaultSelection=""
        routes={[]}
        targets={[target("first")]}
        onValueChange={onValueChange}
      />,
    )
    await userEvent.click(
      screen.getByRole("option", { name: "No default model" }),
    )
    expect(onValueChange).toHaveBeenLastCalledWith("")
  })

  it("lists models by name under their provider's name", async () => {
    const onValueChange = vi.fn()
    render(
      <ModelSelector
        selection=""
        defaultSelection=""
        routes={[]}
        targets={[
          {
            ...target("ext-alpha"),
            target: "ext-alpha/big/free",
            model_id: "big/free",
            label: "Big Model",
            instance_label: "Alpha Service",
          },
        ]}
        onValueChange={onValueChange}
      />,
    )
    expect(screen.getByText(/Alpha Service/)).toBeInTheDocument()
    expect(screen.queryByText(/ext-alpha/i)).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole("option", { name: "Big Model" }))
    expect(onValueChange).toHaveBeenLastCalledWith("ext-alpha/big/free")
  })
})
