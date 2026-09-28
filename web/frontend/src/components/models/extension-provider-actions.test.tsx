import { act, fireEvent, render, screen, waitFor } from "@testing-library/react"
import { toast } from "sonner"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { ExtensionProvider } from "@/api/extension"
import "@/i18n"

import { ExtensionProviderActions } from "./extension-provider-actions"

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

const provider = (extra: Partial<ExtensionProvider>): ExtensionProvider => ({
  id: "demo",
  name: "Account Provider",
  credential: "oauth",
  supported: true,
  instance_id: "ext-demo",
  connected: false,
  ...extra,
})

type Handler = (
  path: string,
  init?: RequestInit,
) => Promise<Response> | undefined

function stubFetch(handler: Handler) {
  const fetchMock = vi.fn(
    (input: RequestInfo | URL, init?: RequestInit) =>
      handler(String(input), init) ??
      Promise.resolve(new Response("missing", { status: 404 })),
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

const calls = (fetchMock: ReturnType<typeof stubFetch>, path: string) =>
  fetchMock.mock.calls.filter(([input]) => String(input) === path)

describe("ExtensionProviderActions", () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
    vi.clearAllMocks()
  })

  it("offers nothing for a provider that needs no key, or that Compa cannot sign in to", () => {
    const { container, rerender } = render(
      <ExtensionProviderActions
        provider={provider({ credential: "none", connected: true })}
        onChanged={vi.fn()}
      />,
    )
    expect(container).toBeEmptyDOMElement()
    rerender(
      <ExtensionProviderActions
        provider={provider({ supported: false })}
        onChanged={vi.fn()}
      />,
    )
    expect(container).toBeEmptyDOMElement()
  })

  it("pastes a token to the provider's token endpoint", async () => {
    const onChanged = vi.fn()
    const fetchMock = stubFetch((path) =>
      path === "/api/extension/providers/beta/token"
        ? json({ status: "ok", instance_id: "ext-beta" })
        : undefined,
    )
    render(
      <ExtensionProviderActions
        provider={provider({
          id: "beta",
          name: "Token Provider",
          credential: "token",
        })}
        onChanged={onChanged}
      />,
    )
    expect(
      screen.queryByRole("button", { name: "Remove token" }),
    ).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Paste token" }))
    const input = screen.getByLabelText("Token for Token Provider")
    expect(input).toHaveAttribute("type", "password")
    fireEvent.change(input, { target: { value: "  secret-value  " } })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    const [call] = calls(fetchMock, "/api/extension/providers/beta/token")
    expect(call?.[1]?.method).toBe("POST")
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({
      token: "secret-value",
    })
    expect(toast.success).toHaveBeenCalledWith("Token saved")
    expect(
      await screen.findByRole("button", { name: "Paste token" }),
    ).toBeInTheDocument()
  })

  it("replaces or removes a stored token", async () => {
    const onChanged = vi.fn()
    const fetchMock = stubFetch((path, init) =>
      path === "/api/extension/providers/beta/credential" &&
      init?.method === "DELETE"
        ? json({ status: "ok" })
        : undefined,
    )
    render(
      <ExtensionProviderActions
        provider={provider({
          id: "beta",
          credential: "token",
          connected: true,
        })}
        onChanged={onChanged}
      />,
    )
    expect(
      screen.getByRole("button", { name: "Replace token" }),
    ).toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Remove token" }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(
      calls(fetchMock, "/api/extension/providers/beta/credential"),
    ).toHaveLength(1)
  })

  it("keeps the token form open and says why when the provider refuses it", async () => {
    const onChanged = vi.fn()
    stubFetch((path) =>
      path === "/api/extension/providers/beta/token"
        ? Promise.resolve(
            new Response("The provider rejected the token.", { status: 502 }),
          )
        : undefined,
    )
    render(
      <ExtensionProviderActions
        provider={provider({ id: "beta", credential: "token" })}
        onChanged={onChanged}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Paste token" }))
    fireEvent.change(screen.getByLabelText("Token for Account Provider"), {
      target: { value: "wrong" },
    })
    fireEvent.click(screen.getByRole("button", { name: "Save" }))
    await waitFor(() =>
      expect(toast.error).toHaveBeenCalledWith(
        "The provider rejected the token.",
      ),
    )
    expect(onChanged).not.toHaveBeenCalled()
    expect(screen.getByLabelText("Token for Account Provider")).toHaveValue(
      "wrong",
    )
  })

  it("signs out of a signed-in account", async () => {
    const onChanged = vi.fn()
    const fetchMock = stubFetch((path, init) =>
      path === "/api/extension/providers/demo/credential" &&
      init?.method === "DELETE"
        ? json({ status: "ok" })
        : undefined,
    )
    render(
      <ExtensionProviderActions
        provider={provider({ connected: true })}
        onChanged={onChanged}
      />,
    )
    expect(
      screen.queryByRole("button", { name: "Sign in" }),
    ).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole("button", { name: "Sign out" }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(
      calls(fetchMock, "/api/extension/providers/demo/credential"),
    ).toHaveLength(1)
  })

  it("shows the device code and polls until approved", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const onChanged = vi.fn()
    let polls = 0
    const fetchMock = stubFetch((path, init) => {
      if (path === "/api/extension/providers/gamma/signin") {
        expect(JSON.parse(String(init?.body))).toEqual({ method: "device" })
        return json({
          flow_id: "f1",
          method: "device",
          status: "pending",
          user_code: "ABCD-1234",
          verification_uri: "https://verify.example.test/device",
          interval_seconds: 1,
        })
      }
      if (path === "/api/extension/signin/f1/poll") {
        polls += 1
        return json({ status: polls < 2 ? "pending" : "approved" })
      }
      return undefined
    })

    render(
      <ExtensionProviderActions
        provider={provider({ id: "gamma", methods: ["device", "manual"] })}
        onChanged={onChanged}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    expect(
      await screen.findByRole("dialog", {
        name: "Sign in to Account Provider",
      }),
    ).toBeInTheDocument()
    fireEvent.click(
      await screen.findByRole("button", { name: "Start sign-in" }),
    )

    expect(await screen.findByTestId("extension-user-code")).toHaveTextContent(
      "ABCD-1234",
    )
    const link = screen.getByRole("link", { name: /Open verification page/ })
    expect(link).toHaveAttribute("href", "https://verify.example.test/device")
    expect(link).toHaveAttribute("rel", "noopener noreferrer")

    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    expect(polls).toBe(1)
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000)
    })
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(polls).toBe(2)
    expect(calls(fetchMock, "/api/extension/signin/f1/poll")).toHaveLength(2)
    expect(toast.success).toHaveBeenCalledWith("Signed in")
    await waitFor(() =>
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
    )
  })

  it("completes a sign-in with a pasted code", async () => {
    const onChanged = vi.fn()
    const fetchMock = stubFetch((path) => {
      if (path === "/api/extension/providers/demo/signin")
        return json({
          flow_id: "f2",
          method: "manual",
          status: "pending",
          authorization_url: "javascript:alert(1)",
        })
      if (path === "/api/extension/signin/f2/complete")
        return json({ status: "approved" })
      return undefined
    })
    render(
      <ExtensionProviderActions
        provider={provider({ methods: ["manual"] })}
        onChanged={onChanged}
      />,
    )
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))
    fireEvent.click(
      await screen.findByRole("button", { name: "Start sign-in" }),
    )
    const code = await screen.findByLabelText(
      "Paste the code or the address of the page you were sent to",
    )
    // A link that is not http(s) is never offered.
    expect(
      screen.queryByRole("link", { name: /Open sign-in page/ }),
    ).not.toBeInTheDocument()
    fireEvent.change(code, { target: { value: " the-code " } })
    fireEvent.click(screen.getByRole("button", { name: "Submit" }))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    const [call] = calls(fetchMock, "/api/extension/signin/f2/complete")
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ code: "the-code" })
  })
})
