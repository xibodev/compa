import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import { toast } from "sonner"
import { afterEach, describe, expect, it, vi } from "vitest"

import "@/i18n"

import { ExtensionSection } from "./extension-section"
import { useExtensionStatus } from "./use-extension-status"

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
    warning: vi.fn(),
  },
}))

const json = (value: unknown) =>
  Promise.resolve(new Response(JSON.stringify(value), { status: 200 }))

const status = {
  url: "http://127.0.0.1:9999",
  has_secret: true,
  status: "connected",
  version: "1.0.0",
  providers: [
    {
      id: "alpha",
      name: "Alpha Service",
      credential: "none",
      supported: true,
      instance_id: "ext-alpha",
      connected: true,
    },
    {
      id: "beta",
      name: "Beta Service",
      credential: "token",
      supported: true,
      instance_id: "ext-beta",
      connected: false,
    },
    {
      id: "gamma",
      name: "Gamma Service",
      credential: "oauth",
      methods: ["device", "manual"],
      supported: true,
      instance_id: "ext-gamma",
      connected: false,
    },
    {
      id: "delta",
      name: "Delta Service",
      credential: "oauth",
      supported: false,
      reason: "Requires a newer daemon",
      connected: false,
    },
  ],
}

type Handler = (
  path: string,
  init?: RequestInit,
) => Promise<Response> | undefined

function stubFetch(handler: Handler, current: unknown = status) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input)
    if (path === "/api/extension" && (!init?.method || init.method === "GET"))
      return json(current)
    return (
      handler(path, init) ??
      Promise.resolve(new Response("missing", { status: 404 }))
    )
  })
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

// The Models page loads the status once and hands it to the section.
function Section({ onChanged }: { onChanged?: () => void }) {
  const extension = useExtensionStatus()
  return <ExtensionSection extension={extension} onChanged={onChanged} />
}

describe("ExtensionSection", () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    vi.clearAllMocks()
  })

  it("names each provider it serves once, with its state and no actions", async () => {
    stubFetch(() => undefined)
    render(<Section />)
    const section = await screen.findByRole("region", { name: "Extension" })
    const alpha = await within(section).findByRole("listitem", {
      name: "Alpha Service",
    })
    expect(alpha).toHaveTextContent("Ready — no sign-in needed")
    expect(
      within(section).getByRole("listitem", { name: "Beta Service" }),
    ).toHaveTextContent("Needs a token")
    expect(
      within(section).getByRole("listitem", { name: "Gamma Service" }),
    ).toHaveTextContent("Needs sign-in")
    expect(
      within(section).getByRole("listitem", { name: "Delta Service" }),
    ).toHaveTextContent("Requires a newer daemon")

    // Signing in happens on each provider's card, which the hint points to.
    for (const item of within(section).getAllByRole("listitem")) {
      expect(within(item).queryByRole("button")).not.toBeInTheDocument()
    }
    expect(
      within(section).queryByRole("button", {
        name: /Sign in|Paste token|Sign out|Remove token/,
      }),
    ).not.toBeInTheDocument()
    expect(section).toHaveTextContent(
      "To sign in or add a token, open the provider's card above.",
    )
    // The connection block stays.
    expect(section).toHaveTextContent(
      "Connected · http://127.0.0.1:9999 · version 1.0.0",
    )
    expect(
      within(section).getByRole("button", { name: "Edit connection" }),
    ).toBeInTheDocument()
    expect(
      within(section).getByRole("button", { name: "Disconnect" }),
    ).toBeInTheDocument()
  })

  it("starts a first connection at the default address and confirms it", async () => {
    const onChanged = vi.fn()
    const fetchMock = stubFetch(
      (path, init) =>
        path === "/api/extension" && init?.method === "PUT"
          ? json(status)
          : undefined,
      { has_secret: false, status: "not_configured", providers: [] },
    )
    render(<Section onChanged={onChanged} />)
    expect(await screen.findByText("Not connected")).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Connect" }))
    const dialog = await screen.findByRole("dialog", {
      name: "Connect the extension",
    })
    const address = within(dialog).getByLabelText("Address")
    expect(address).toHaveValue("http://127.0.0.1:18888")
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    const put = fetchMock.mock.calls.find(
      ([path, init]) =>
        String(path) === "/api/extension" && init?.method === "PUT",
    )
    expect(JSON.parse(String(put?.[1]?.body))).toEqual({
      url: "http://127.0.0.1:18888",
    })
    expect(toast.success).toHaveBeenCalledWith(
      "Connected to the extension. It offers 4 providers.",
    )
    // The status the server returned shows without another load.
    expect(
      await screen.findByRole("listitem", { name: "Alpha Service" }),
    ).toBeInTheDocument()
  })

  it("disconnects after asking and reloads", async () => {
    const onChanged = vi.fn()
    let connected = true
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === "/api/extension" && init?.method === "DELETE") {
        connected = false
        return json({ status: "ok" })
      }
      if (path === "/api/extension")
        return json(
          connected
            ? status
            : { has_secret: false, status: "not_configured", providers: [] },
        )
      return Promise.resolve(new Response("missing", { status: 404 }))
    })
    vi.stubGlobal("fetch", fetchMock)
    render(<Section onChanged={onChanged} />)
    fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }))
    const confirm = await screen.findByRole("alertdialog")
    fireEvent.click(within(confirm).getByRole("button", { name: "Disconnect" }))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(await screen.findByText("Not connected")).toBeInTheDocument()
    expect(screen.queryByRole("listitem")).not.toBeInTheDocument()
  })
})
