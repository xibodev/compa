import { fireEvent, render, screen } from "@testing-library/react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { ModuleView } from "@/api/modules"
import "@/i18n"

import { ModulesPage } from "./modules-page"

vi.mock("@/components/ui/sidebar", () => ({ SidebarTrigger: () => null }))

const MODULE: ModuleView = {
  module: "video",
  name: "Video",
  version: "1.0.0",
  binary: "/modules/video/video",
  capabilities: [
    {
      id: "render",
      title: "Render",
      summary: "Renders a clip.",
      local: false,
      network: true,
      external_writes: false,
      provider: "",
      cost_known: false,
      tool_name: "module_video_render",
      needs_approval: true,
    },
  ],
  overlays: 0,
  skills: 0,
  permissions: {
    filesystem_read: [],
    filesystem_write: [],
    network: [],
    credentials: [],
    paid_providers: [],
    publish: false,
    subprocess: [],
  },
  requirements: [],
  warnings: [],
}

const reply = (value: unknown) =>
  Promise.resolve(new Response(JSON.stringify(value), { status: 200 }))

beforeEach(() => {
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL) => {
      const path = String(input)
      if (path === "/api/modules") return reply([MODULE])
      return reply({ modules_dir: "/modules" })
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

describe("ModulesPage", () => {
  it("leaves approvals to the policy and keeps Approve and run", async () => {
    render(<ModulesPage />)
    fireEvent.click(await screen.findByRole("button", { name: /^render/ }))

    // The capability the policy asks about is approved here by running it.
    expect(
      screen.getByRole("button", { name: "Approve and run" }),
    ).toBeInTheDocument()
    expect(screen.getByText(/records your approval/)).toBeInTheDocument()
    // The page has no approval setting of its own.
    expect(screen.queryByRole("combobox")).not.toBeInTheDocument()
  })
})
