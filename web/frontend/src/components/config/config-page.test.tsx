import { QueryClient, QueryClientProvider } from "@tanstack/react-query"
import {
  type BoundFunctions,
  fireEvent,
  type queries,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import {
  type ReactElement,
  type ReactNode,
  cloneElement,
  createContext,
  useContext,
  useState,
} from "react"
import { toast } from "sonner"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import { ConfigPage } from "./config-page"

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="/">{children}</a>,
  useBlocker: () => ({ status: "idle" }),
}))
vi.mock("@/components/ui/sidebar", () => ({ SidebarTrigger: () => null }))
vi.mock("@/store/gateway", () => ({
  refreshGatewayState: vi.fn(async () => null),
}))
vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn(), warning: vi.fn() },
}))

// Radix positions its menu with a popper that keeps jsdom busy for many
// seconds, so the menu is replaced by one that shows its items when open.
vi.mock("@/components/ui/dropdown-menu", () => {
  const Open = createContext<[boolean, (open: boolean) => void]>([
    false,
    () => {},
  ])
  return {
    DropdownMenu: function DropdownMenu({ children }: { children: ReactNode }) {
      const state = useState(false)
      return <Open.Provider value={state}>{children}</Open.Provider>
    },
    DropdownMenuTrigger: function DropdownMenuTrigger({
      children,
      disabled,
    }: {
      children: ReactElement<{ disabled?: boolean; onClick?: () => void }>
      disabled?: boolean
    }) {
      const [open, setOpen] = useContext(Open)
      return cloneElement(children, { disabled, onClick: () => setOpen(!open) })
    },
    DropdownMenuContent: function DropdownMenuContent({
      children,
    }: {
      children: ReactNode
    }) {
      const [open] = useContext(Open)
      return open ? <div role="menu">{children}</div> : null
    },
    DropdownMenuCheckboxItem: function DropdownMenuCheckboxItem({
      children,
      checked,
      onCheckedChange,
    }: {
      children: ReactNode
      checked?: boolean
      onCheckedChange?: (checked: boolean) => void
    }) {
      return (
        <div
          role="menuitemcheckbox"
          aria-checked={checked}
          tabIndex={-1}
          onClick={() => onCheckedChange?.(!checked)}
        >
          {children}
        </div>
      )
    },
  }
})

const APPROVAL = {
  default: "allow",
  rules: [
    {
      source: "module:*",
      hints: ["cost_unknown", "network", "external_writes"],
      action: "ask",
    },
    { tool: "install_skill", action: "ask" },
    { tool: "exec", origin: ["chat", "cron"], action: "deny" },
  ],
}

const CONFIG = {
  agents: { defaults: { workspace: "~/.compa/workspace" } },
  session: { dm_scope: "per-channel-peer" },
  commands: { owner_only: false },
  logging: { redact_secrets: true, max_size_mb: 10, max_files: 5 },
  tools: {
    approval: APPROVAL,
    exec: { enabled: true },
    mcp: {
      enabled: true,
      servers: { github: { command: "gh-mcp", env: { TOKEN: "[NOT_HERE]" } } },
    },
  },
}

const LAUNCHER = {
  port: 18800,
  public: false,
  allowed_cidrs: [],
  allow_localhost_bypass: true,
  trusted_proxy_cidrs: [],
  allowed_hosts: [],
  allow_lan_without_password: false,
  remote_images: "click",
}

const reply = (value: unknown, status = 200) =>
  Promise.resolve(new Response(JSON.stringify(value), { status }))

let requests: { method: string; path: string; body?: unknown }[] = []
let config: unknown = CONFIG

beforeEach(() => {
  requests = []
  config = CONFIG
  vi.mocked(toast.error).mockClear()
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      const method = init?.method ?? "GET"
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      requests.push({ method, path, body })
      if (path === "/api/config" && method === "GET") return reply(config)
      if (path === "/api/system/launcher-config")
        return reply(method === "PUT" ? { ...LAUNCHER, ...body } : LAUNCHER)
      if (path === "/api/system/version")
        return reply({ version: "dev", go_version: "go" })
      if (path === "/api/system/autostart")
        return reply({ enabled: false, supported: true, platform: "test" })
      if (path === "/api/config/test-command-patterns")
        return reply(
          {
            error: "invalid pattern",
            invalid_patterns: ['"(": missing closing )'],
          },
          400,
        )
      return reply({ status: "ok" })
    }),
  )
})

afterEach(() => vi.unstubAllGlobals())

const sent = (method: string, path: string) =>
  requests.filter((r) => r.method === method && r.path === path)

const savedConfig = () =>
  sent("PATCH", "/api/config")[0].body as {
    commands: unknown
    logging: unknown
    tools: {
      approval: unknown
      exec: Record<string, unknown>
      mcp: { servers: { github: { env: unknown } } }
    }
  }

type Scope = BoundFunctions<typeof queries>

/**
 * The Approvals card, to query within: role queries over the whole page
 * take seconds each under jsdom.
 */
async function approvalsCard(): Promise<Scope> {
  const list = await screen.findByRole("list", { name: "Approvals" })
  return within(list.closest<HTMLElement>("[data-slot=card]")!)
}

/** The rows of the Approvals list, to query each within. */
const rowsOf = (card: Scope): Scope[] =>
  card.getAllByRole("listitem").map((row) => within(row))

/** Picks an option of a select. */
async function choose(select: HTMLElement, option: string) {
  fireEvent.keyDown(select, { key: "Enter" })
  // The open list is found through its trigger: a role query over the
  // whole page would be slow.
  const list = await waitFor(() =>
    within(document.getElementById(select.getAttribute("aria-controls")!)!),
  )
  fireEvent.click(list.getByRole("option", { name: option }))
}

/** Ticks options of one of a row's multi-value fields. */
function tick(row: Scope, field: string, ...options: string[]) {
  const trigger = row.getByRole("button", { name: field })
  fireEvent.click(trigger)
  const menu = within(row.getByRole("menu"))
  for (const option of options) {
    fireEvent.click(menu.getByRole("menuitemcheckbox", { name: option }))
  }
  fireEvent.click(trigger)
}

function renderPage() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ConfigPage />
    </QueryClientProvider>,
  )
}

describe("ConfigPage", () => {
  it("saves the command and logging settings with the config", async () => {
    renderPage()
    const ownerOnly = await screen.findByRole("switch", {
      name: "Owner-Only Commands",
    })
    expect(ownerOnly).not.toBeChecked()

    fireEvent.click(ownerOnly)
    fireEvent.change(screen.getByLabelText("Files Kept"), {
      target: { value: "3" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(sent("PATCH", "/api/config")).toHaveLength(1))
    const patch = savedConfig()
    expect(patch.commands).toEqual({ owner_only: true })
    expect(patch.logging).toEqual({
      redact_secrets: true,
      max_size_mb: 10,
      max_files: 3,
    })
    // Commands have no approval setting of their own; the untouched
    // approval rules go back as they came.
    expect(patch.tools.exec).not.toHaveProperty("approval")
    expect(patch.tools.approval).toEqual(APPROVAL)
    // The hidden secret goes back as the placeholder, which keeps it.
    expect(patch.tools.mcp.servers.github.env).toEqual({
      TOKEN: "[NOT_HERE]",
    })
  })

  it("lists the approval rules in their order, and the default", async () => {
    renderPage()
    const card = await approvalsCard()
    const rows = rowsOf(card)
    expect(rows).toHaveLength(3)
    const [modules, install, exec] = rows

    expect(modules.getByRole("textbox", { name: "Tool" })).toHaveValue("")
    expect(modules.getByRole("textbox", { name: "Source" })).toHaveValue(
      "module:*",
    )
    expect(modules.getByRole("button", { name: "From" })).toHaveTextContent("*")
    expect(modules.getByRole("button", { name: "Hints" })).toHaveTextContent(
      "Cost unknown, Network, External writes",
    )
    expect(modules.getByRole("combobox", { name: "Action" })).toHaveTextContent(
      "Ask",
    )
    expect(install.getByRole("textbox", { name: "Tool" })).toHaveValue(
      "install_skill",
    )
    expect(exec.getByRole("button", { name: "From" })).toHaveTextContent(
      "Chat, Scheduled",
    )
    expect(exec.getByRole("combobox", { name: "Action" })).toHaveTextContent(
      "Deny",
    )
    expect(card.getByRole("combobox", { name: "Default" })).toHaveTextContent(
      "Allow",
    )
  })

  it("adds, edits and removes approval rules, and saves the list in order", async () => {
    renderPage()
    const card = await approvalsCard()
    const [first, second] = rowsOf(card)
    fireEvent.change(first.getByRole("textbox", { name: "Source" }), {
      target: { value: "module:video" },
    })
    fireEvent.click(second.getByRole("button", { name: "Remove" }))
    fireEvent.click(card.getByRole("button", { name: "Add rule" }))

    const rows = rowsOf(card)
    expect(rows).toHaveLength(3)
    // A new rule goes last, and asks.
    const added = rows[2]
    expect(added.getByRole("combobox", { name: "Action" })).toHaveTextContent(
      "Ask",
    )
    fireEvent.change(added.getByRole("textbox", { name: "Tool" }), {
      target: { value: "mcp_github_*" },
    })
    tick(added, "From", "Chat", "Web")
    tick(added, "Hints", "Destructive")
    await choose(added.getByRole("combobox", { name: "Action" }), "Deny")
    await choose(card.getByRole("combobox", { name: "Default" }), "Ask")
    expect(added.getByRole("button", { name: "From" })).toHaveTextContent(
      "Web, Chat",
    )

    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(sent("PATCH", "/api/config")).toHaveLength(1))
    expect(savedConfig().tools.approval).toEqual({
      default: "ask",
      rules: [
        {
          source: "module:video",
          hints: ["cost_unknown", "network", "external_writes"],
          action: "ask",
        },
        { tool: "exec", origin: ["chat", "cron"], action: "deny" },
        {
          tool: "mcp_github_*",
          origin: ["web", "chat"],
          hints: ["destructive"],
          action: "deny",
        },
      ],
    })
  })

  it("keeps a rule it cannot show as it came", async () => {
    const unknown = { tool: "exec", origin: ["email"], action: "ask" }
    config = {
      ...CONFIG,
      tools: {
        ...CONFIG.tools,
        approval: { rules: [unknown, { tool: "spawn", action: "deny" }] },
      },
    }
    renderPage()
    const card = await approvalsCard()
    const [kept, other] = rowsOf(card)
    expect(kept.getByRole("textbox", { name: "Tool" })).toBeDisabled()
    expect(kept.getByRole("button", { name: "From" })).toBeDisabled()
    expect(kept.getByRole("button", { name: "From" })).toHaveTextContent(
      "email",
    )
    expect(kept.getByRole("combobox", { name: "Action" })).toBeDisabled()
    // An unset default allows.
    expect(card.getByRole("combobox", { name: "Default" })).toHaveTextContent(
      "Allow",
    )

    fireEvent.change(other.getByRole("textbox", { name: "Tool" }), {
      target: { value: "spawn_*" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() => expect(sent("PATCH", "/api/config")).toHaveLength(1))
    expect(savedConfig().tools.approval).toEqual({
      default: "allow",
      rules: [unknown, { tool: "spawn_*", action: "deny" }],
    })
  })

  it("saves the access settings and changes the password with the current one", async () => {
    renderPage()
    fireEvent.change(await screen.findByLabelText("Allowed Hosts"), {
      target: { value: "compa.example.test\nproxy.local" },
    })
    fireEvent.click(
      screen.getByRole("switch", { name: "Allow LAN Without Password" }),
    )
    expect(screen.queryByLabelText("Current Password")).not.toBeInTheDocument()
    fireEvent.change(screen.getByLabelText("Login Password"), {
      target: { value: "new-password-1" },
    })
    fireEvent.change(screen.getByLabelText("Confirm New Password"), {
      target: { value: "new-password-1" },
    })
    fireEvent.change(screen.getByLabelText("Current Password"), {
      target: { value: "old-password" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(sent("POST", "/api/auth/setup")).toHaveLength(1))
    expect(sent("PUT", "/api/system/launcher-config")[0].body).toMatchObject({
      allowed_hosts: ["compa.example.test", "proxy.local"],
      allow_lan_without_password: true,
      remote_images: "click",
    })
    expect(sent("POST", "/api/auth/setup")[0].body).toEqual({
      password: "new-password-1",
      confirm: "new-password-1",
      current_password: "old-password",
    })
  })

  it("asks for the current password before changing it", async () => {
    renderPage()
    fireEvent.change(await screen.findByLabelText("Login Password"), {
      target: { value: "new-password-1" },
    })
    fireEvent.change(screen.getByLabelText("Confirm New Password"), {
      target: { value: "new-password-1" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "Enter the current login password.",
      ),
    )
    expect(sent("POST", "/api/auth/setup")).toHaveLength(0)
  })

  it("names the patterns the command test could not compile", async () => {
    renderPage()
    fireEvent.change(
      await screen.findByRole("textbox", { name: "Pattern Detection Tool" }),
      { target: { value: "rm -rf /" } },
    )
    fireEvent.click(screen.getByRole("button", { name: "Test" }))

    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        'Invalid patterns: "(": missing closing )',
      ),
    )
  })
})
