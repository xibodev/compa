import { render, screen, waitFor, within } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import type { ReactNode } from "react"
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"

import type { ExtensionProvider, ExtensionStatus } from "@/api/extension"
import type {
  ProviderInstance,
  ProviderInstanceCatalog,
  ProviderInstanceRuntime,
  ProviderRosterEntry,
  ProviderTarget,
} from "@/api/provider-instances"
import { SidebarProvider } from "@/components/ui/sidebar"
import "@/i18n"

import { ModelsWorkspace } from "./models-workspace"
import { FREE_TEST_STORAGE_KEY } from "./provider-model"

vi.mock("@tanstack/react-router", () => ({
  Link: ({
    to,
    children,
    className,
  }: {
    to: string
    children: ReactNode
    className?: string
  }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}))

const reply = (value: unknown, status = 200) =>
  Promise.resolve(
    typeof value === "string"
      ? new Response(value, { status })
      : new Response(JSON.stringify(value), { status }),
  )

const instance = (
  id: string,
  runtime?: ProviderInstanceRuntime,
  extra: Partial<ProviderInstance> = {},
): ProviderInstance => ({
  id,
  provider_kind: "openai",
  adapter: "openai-compatible",
  protocol: "openai",
  endpoint: `https://${id}.test/v1`,
  auth_configured: true,
  header_names: [],
  setting_names: [],
  ...(runtime ? { runtime } : {}),
  state: "enabled",
  ...extra,
})

// An instance the extension serves, as the server lists it.
const managed = (
  id: string,
  extra: Partial<ProviderInstance>,
): ProviderInstance =>
  instance(id, undefined, {
    provider_kind: "extension",
    adapter: "extension",
    protocol: "chat_completions",
    endpoint: "http://127.0.0.1:18888",
    auth_configured: false,
    managed_by: "extension",
    ...extra,
  })

const target = (
  instanceID: string,
  modelID = "shared",
  extra: Partial<ProviderTarget> = {},
): ProviderTarget => ({
  target: `${instanceID}/${modelID}`,
  instance_id: instanceID,
  model_id: modelID,
  provider_kind: "openai",
  fetched_at: "",
  ...extra,
})

const catalog = (
  instanceID: string,
  models: ProviderInstanceCatalog["models"],
): ProviderInstanceCatalog => ({
  instance_id: instanceID,
  provider_kind: "openai",
  models,
  fetched_at: "",
})

const NO_EXTENSION: ExtensionStatus = {
  has_secret: false,
  status: "not_configured",
  providers: [],
}

const extensionWith = (providers: ExtensionProvider[]): ExtensionStatus => ({
  url: "http://127.0.0.1:18888",
  has_secret: false,
  status: "connected",
  version: "1.0.0",
  providers,
})

let defaultSelection = ""
let requests: { path: string; method: string; body?: unknown }[] = []
let instances: ProviderInstance[] = []
let roster: ProviderRosterEntry[] = []
let targets: ProviderTarget[] = []
let catalogs: ProviderInstanceCatalog[] = []
let extension: ExtensionStatus = NO_EXTENSION
let freeTestReply: unknown = {}

const writes = (path: string) =>
  requests.filter(
    (request) => request.path === path && request.method !== "GET",
  )

function fakeServer(input: RequestInfo | URL, init?: RequestInit) {
  const path = String(input)
  const method = init?.method ?? "GET"
  const body = init?.body ? JSON.parse(String(init.body)) : undefined
  requests.push({ path, method, body })
  if (path === "/api/default-model") {
    if (method === "PUT") {
      const selection = String(body.selection)
      if (
        selection &&
        !["first/shared", "second/shared", "primary"].includes(selection)
      )
        return reply("selection does not resolve", 400)
      defaultSelection = selection
    }
    return reply({ selection: defaultSelection })
  }
  if (path === "/api/provider-instances" && method === "POST") {
    instances = [
      ...instances,
      instance(String(body.id), undefined, {
        provider_kind: String(body.provider_kind),
        endpoint: String(body.endpoint),
      }),
    ]
    return reply({ status: "ok" })
  }
  if (path === "/api/provider-instances") return reply({ instances })
  if (path === "/api/provider-instances/auto-connect-free")
    return reply(freeTestReply)
  if (path.endsWith("/catalog/sync"))
    return reply({
      instance_id: "x",
      models: [{ id: "a" }, { id: "b" }],
      total: 2,
    })
  if (path.startsWith("/api/provider-instances/") && method === "PUT")
    return reply({ status: "ok" })
  if (path === "/api/provider-targets?all=true") return reply({ targets })
  if (path === "/api/active-models")
    return reply({ active_models: ["first/shared"], total: 1 })
  if (path === "/api/provider-instances/catalogs") return reply({ catalogs })
  if (path === "/api/model-routes")
    return reply({
      routes: [{ name: "primary", targets: ["second/shared", "first/shared"] }],
    })
  if (path === "/api/provider-roster") return reply({ providers: roster })
  if (path === "/api/extension") return reply(extension)
  const token = /^\/api\/extension\/providers\/([^/]+)\/token$/.exec(path)
  if (token && method === "POST") {
    // The provider takes the token: its instance is ready and enabled.
    const id = `ext-${token[1]}`
    instances = instances.map((item) =>
      item.id === id
        ? { ...item, state: "enabled", credential_ready: true }
        : item,
    )
    extension = {
      ...extension,
      providers: extension.providers.map((provider) =>
        provider.instance_id === id
          ? { ...provider, connected: true }
          : provider,
      ),
    }
    return reply({ status: "ok", instance_id: id })
  }
  return reply({})
}

describe("ModelsWorkspace", () => {
  beforeEach(() => {
    defaultSelection = ""
    requests = []
    instances = [
      instance("first"),
      instance("second", { rpm: 30, extra_body: { reasoning_split: true } }),
    ]
    roster = [
      {
        id: "unknown",
        display_name: "Unknown protocol",
        compatibility: "discovery_only",
      },
    ]
    targets = [target("first"), target("second")]
    catalogs = ["first", "second"].map((id) => catalog(id, [{ id: "shared" }]))
    extension = NO_EXTENSION
    freeTestReply = {}
    localStorage.clear()
    vi.stubGlobal("fetch", vi.fn(fakeServer))
  })
  afterEach(() => vi.unstubAllGlobals())

  const renderWorkspace = () =>
    render(
      <SidebarProvider>
        <ModelsWorkspace />
      </SidebarProvider>,
    )

  const defaultBar = () => screen.getByRole("region", { name: "Default model" })

  it("names its tabs plainly", async () => {
    renderWorkspace()
    expect(
      (await screen.findAllByRole("tab")).map((tab) => tab.textContent),
    ).toEqual(["Providers", "Models & Routes"])
  })

  it("adds providers only from the provider list, with an API key", async () => {
    roster = [
      {
        id: "openai",
        display_name: "OpenAI",
        adapter: "openai-compatible",
        protocol: "openai",
        default_endpoint: "https://api.openai.com/v1",
        compatibility: "compatible",
        auth_methods: ["api_key"],
        requires_api_key: true,
      },
    ]
    instances = []
    const user = userEvent.setup()
    renderWorkspace()

    // No separate key cards and no free-standing "Custom" button.
    expect(
      await screen.findByRole("button", { name: "Connect OpenAI" }),
    ).toBeInTheDocument()
    expect(screen.queryByRole("group", { name: /connection$/ })).toBeNull()
    expect(screen.queryByRole("button", { name: "Custom" })).toBeNull()

    await user.click(screen.getByRole("button", { name: "Connect OpenAI" }))
    const dialog = await screen.findByRole("dialog", { name: "Connect OpenAI" })
    const scope = within(dialog)
    await user.click(scope.getByRole("button", { name: "Connect provider" }))
    expect(await scope.findByText("Enter your API key.")).toBeInTheDocument()
    expect(writes("/api/provider-instances")).toHaveLength(0)

    await user.type(scope.getByLabelText("API key"), "sk-test")
    await user.click(scope.getByRole("button", { name: "Connect provider" }))
    await waitFor(() =>
      expect(writes("/api/provider-instances")).toHaveLength(1),
    )
    expect(writes("/api/provider-instances")[0].body).toMatchObject({
      id: "openai",
      provider_kind: "openai",
      endpoint: "https://api.openai.com/v1",
      write_only: { api_key: "sk-test" },
    })
    // The new connection's models load right away.
    await waitFor(() =>
      expect(
        writes("/api/provider-instances/openai/catalog/sync"),
      ).toHaveLength(1),
    )
  })

  it("shows discovery-only providers without a connect action", async () => {
    renderWorkspace()
    expect(await screen.findByText("Unknown protocol")).toBeInTheDocument()
    expect(
      screen.queryByRole("button", { name: "Connect provider" }),
    ).not.toBeInTheDocument()
  })

  describe("providers the extension serves", () => {
    beforeEach(() => {
      instances = [
        managed("ext-alpha", {
          display_name: "Alpha Free",
          credential_kind: "none",
          credential_ready: true,
        }),
        managed("ext-beta", {
          display_name: "Beta Token",
          state: "disabled",
          credential_kind: "token",
          credential_ready: false,
        }),
        managed("ext-gamma", {
          display_name: "Gamma Account",
          state: "disabled",
          credential_kind: "oauth",
          credential_ready: false,
        }),
        instance("ready-but-off", undefined, {
          display_name: "Paused Provider",
          state: "disabled",
          credential_kind: "api_key",
          credential_ready: true,
        }),
      ]
      catalogs = [
        catalog("ext-alpha", [
          { id: "tiny", display_name: "Tiny", surfaces: ["chat_completions"] },
        ]),
      ]
      targets = [
        target("ext-alpha", "tiny", {
          label: "Tiny",
          instance_label: "Alpha Free",
          surfaces: ["chat_completions"],
        }),
      ]
      extension = extensionWith([
        {
          id: "alpha",
          name: "Alpha Free",
          credential: "none",
          supported: true,
          instance_id: "ext-alpha",
          connected: true,
        },
        {
          id: "beta",
          name: "Beta Token",
          credential: "token",
          supported: true,
          instance_id: "ext-beta",
          connected: false,
        },
        {
          id: "gamma",
          name: "Gamma Account",
          credential: "oauth",
          methods: ["device"],
          supported: true,
          instance_id: "ext-gamma",
          connected: false,
        },
      ])
    })

    it("tells truthfully what each connection can do", async () => {
      const user = userEvent.setup()
      renderWorkspace()

      // Display names, never raw instance ids.
      const alpha = await screen.findByRole("button", {
        name: "Manage Alpha Free",
      })
      expect(screen.queryByText("ext-alpha")).not.toBeInTheDocument()
      // A keyless provider an extension serves is listed with the free ones.
      const free = screen.getByRole("region", { name: "Free — no key needed" })
      expect(
        within(free).getByRole("button", { name: "Manage Alpha Free" }),
      ).toBe(alpha)
      expect(alpha).toHaveTextContent("Connected")

      const gamma = screen.getByRole("button", { name: "Manage Gamma Account" })
      expect(gamma).toHaveTextContent("Needs sign-in")
      expect(gamma).not.toHaveTextContent("Connected")
      const beta = screen.getByRole("button", { name: "Manage Beta Token" })
      expect(beta).toHaveTextContent("Needs a token")
      expect(beta).not.toHaveTextContent("Connected")
      expect(
        screen.getByRole("button", { name: "Manage Paused Provider" }),
      ).toHaveTextContent("Disabled")

      // Only the usable provider counts as connected, and only it is
      // listed under that filter.
      const connected = screen.getByRole("button", { name: "Connected (1)" })
      await user.click(connected)
      expect(
        screen.getByRole("button", { name: "Manage Alpha Free" }),
      ).toBeInTheDocument()
      for (const name of ["Gamma Account", "Beta Token", "Paused Provider"])
        expect(
          screen.queryByRole("button", { name: `Manage ${name}` }),
        ).not.toBeInTheDocument()
    })

    it("offers a provider's own sign-in on its card, and nothing the extension owns", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      await user.click(
        await screen.findByRole("button", { name: "Manage Gamma Account" }),
      )
      const inspector = screen.getByRole("region", { name: "Gamma Account" })
      const scope = within(inspector)
      expect(
        await scope.findByRole("button", { name: "Sign in" }),
      ).toBeInTheDocument()
      for (const name of [
        /Go to Extension/,
        /Edit/,
        /Test connection/,
        /Refresh models/,
        /Remove/,
        /Add another connection/,
      ])
        expect(scope.queryByRole("button", { name })).not.toBeInTheDocument()
      expect(inspector).toHaveTextContent("Served by the extension")
      expect(inspector).toHaveTextContent("Account sign-in")
      expect(inspector).toHaveTextContent("Used forChat")
      // No address, no provider type and no raw surface id.
      expect(inspector).not.toHaveTextContent("127.0.0.1")
      expect(inspector).not.toHaveTextContent("chat_completions")
      expect(scope.queryByText("Type")).not.toBeInTheDocument()
      expect(inspector).not.toHaveTextContent(/Extension section below/)

      await user.click(scope.getByRole("button", { name: "Sign in" }))
      expect(
        await screen.findByRole("dialog", { name: "Sign in to Gamma Account" }),
      ).toBeInTheDocument()
    })

    it("pastes a provider's token from its card", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      await user.click(
        await screen.findByRole("button", { name: "Manage Beta Token" }),
      )
      const inspector = screen.getByRole("region", { name: "Beta Token" })
      const scope = within(inspector)
      await user.click(
        await scope.findByRole("button", { name: "Paste token" }),
      )
      await user.type(scope.getByLabelText("Token for Beta Token"), "tok-123")
      await user.click(scope.getByRole("button", { name: "Save" }))

      await waitFor(() =>
        expect(writes("/api/extension/providers/beta/token")).toHaveLength(1),
      )
      expect(writes("/api/extension/providers/beta/token")[0].body).toEqual({
        token: "tok-123",
      })
      // Both the extension status and the provider list reload: the card
      // is now connected, can replace its token and refresh its models.
      await waitFor(() =>
        expect(
          screen.getByRole("button", { name: "Manage Beta Token" }),
        ).toHaveTextContent("Connected"),
      )
      expect(
        await scope.findByRole("button", { name: "Replace token" }),
      ).toBeInTheDocument()
      expect(scope.getByRole("button", { name: "Remove token" })).toBeVisible()
      expect(
        scope.getByRole("button", { name: "Refresh models" }),
      ).toBeInTheDocument()
    })

    it("refreshes a ready keyless provider's models, and offers no sign-in", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      await user.click(
        await screen.findByRole("button", { name: "Manage Alpha Free" }),
      )
      const scope = within(screen.getByRole("region", { name: "Alpha Free" }))
      expect(
        scope.getByRole("button", { name: "Refresh models" }),
      ).toBeInTheDocument()
      expect(
        scope.getByRole("button", { name: "Add to Chat" }),
      ).toBeInTheDocument()
      for (const name of [/Sign in/, /Paste token/, /Test connection/, /Edit/])
        expect(scope.queryByRole("button", { name })).not.toBeInTheDocument()
    })

    it("says sign-in needs the extension while it is not connected", async () => {
      extension = NO_EXTENSION
      const user = userEvent.setup()
      renderWorkspace()
      await user.click(
        await screen.findByRole("button", { name: "Manage Gamma Account" }),
      )
      const inspector = screen.getByRole("region", { name: "Gamma Account" })
      expect(
        await within(inspector).findByText(
          "Sign-in needs the extension. Connect it in the Extension section below.",
        ),
      ).toBeInTheDocument()
      expect(
        within(inspector).queryByRole("button", { name: /Sign in|Edit/ }),
      ).not.toBeInTheDocument()
    })

    it("says so when the extension no longer offers a provider", async () => {
      extension = extensionWith(
        extension.providers.filter((provider) => provider.id !== "gamma"),
      )
      const user = userEvent.setup()
      renderWorkspace()
      await user.click(
        await screen.findByRole("button", { name: "Manage Gamma Account" }),
      )
      const inspector = screen.getByRole("region", { name: "Gamma Account" })
      expect(
        await within(inspector).findByText(
          "The extension no longer offers this provider.",
        ),
      ).toBeInTheDocument()
      expect(inspector).not.toHaveTextContent(/Connect it in/)
      expect(
        within(inspector).queryByRole("button", { name: /Sign in/ }),
      ).not.toBeInTheDocument()
    })
  })

  it("counts a speech provider's voices and points to the Voice page", async () => {
    instances = [
      managed("ext-speech", {
        display_name: "Example Voice",
        protocol: "audio_speech",
        credential_kind: "none",
        credential_ready: true,
      }),
    ]
    catalogs = [
      catalog(
        "ext-speech",
        Array.from({ length: 321 }, (_, index) => ({
          id: `demo_voice_${index}`,
          surfaces: ["audio_speech"],
        })),
      ),
    ]
    const user = userEvent.setup()
    renderWorkspace()
    await user.click(
      await screen.findByRole("button", { name: "Manage Example Voice" }),
    )
    const inspector = screen.getByRole("region", { name: "Example Voice" })
    expect(inspector).toHaveTextContent("321 voices")
    expect(inspector).toHaveTextContent("Used forText to speech")
    expect(
      within(inspector).getByRole("link", {
        name: "Choose a voice on the Voice page",
      }),
    ).toHaveAttribute("href", "/config/voice")
    // Not one voice is listed.
    expect(within(inspector).queryByRole("listitem")).not.toBeInTheDocument()
    expect(inspector).not.toHaveTextContent("demo_voice_0")
  })

  it("searches a long catalog and shows it fifty models at a time", async () => {
    catalogs = [
      catalog(
        "first",
        Array.from({ length: 120 }, (_, index) => ({
          id: `model-${String(index).padStart(3, "0")}`,
        })),
      ),
    ]
    const user = userEvent.setup()
    renderWorkspace()
    await user.click(
      await screen.findByRole("button", { name: "Manage first" }),
    )
    const inspector = screen.getByRole("region", { name: "first" })
    const scope = within(inspector)
    expect(scope.getAllByRole("listitem")).toHaveLength(50)
    expect(inspector).toHaveTextContent("Showing 50 of 120")
    await user.click(scope.getByRole("button", { name: "Show more" }))
    expect(scope.getAllByRole("listitem")).toHaveLength(100)
    await user.click(scope.getByRole("button", { name: "Show more" }))
    expect(scope.getAllByRole("listitem")).toHaveLength(120)
    expect(
      scope.queryByRole("button", { name: "Show more" }),
    ).not.toBeInTheDocument()

    await user.type(
      scope.getByRole("searchbox", { name: "Search models" }),
      "model-11",
    )
    expect(
      scope.getAllByRole("listitem").map((item) => item.textContent),
    ).toEqual(
      Array.from({ length: 10 }, (_, index) =>
        expect.stringContaining(`model-11${index}`),
      ),
    )
    await user.clear(scope.getByRole("searchbox", { name: "Search models" }))
    await user.type(
      scope.getByRole("searchbox", { name: "Search models" }),
      "nothing-like-it",
    )
    expect(scope.getByText("No models match your search.")).toBeVisible()
  })

  it("lists a short catalog whole, without a search box", async () => {
    renderWorkspace()
    const user = userEvent.setup()
    await user.click(
      await screen.findByRole("button", { name: "Manage first" }),
    )
    const scope = within(screen.getByRole("region", { name: "first" }))
    expect(scope.getAllByRole("listitem")).toHaveLength(1)
    expect(
      scope.queryByRole("searchbox", { name: "Search models" }),
    ).not.toBeInTheDocument()
  })

  it("keeps the free provider test's results until they are dismissed", async () => {
    freeTestReply = {
      ok: true,
      total: 2,
      catalog_discovered: 2,
      verified: 1,
      instances: ["alpha-free"],
      outcomes: [
        {
          registry_id: "alpha_free",
          provider_id: "alpha-free",
          status: "verified",
          probe_model: "tiny",
          latency_ms: 120,
        },
        {
          registry_id: "beta_free",
          provider_id: "beta-free",
          status: "failed",
          error_class: "rate_limited",
          error: "429 too many requests",
        },
      ],
    }
    const user = userEvent.setup()
    const { unmount } = renderWorkspace()
    await user.click(
      await screen.findByRole("button", { name: "Try free providers" }),
    )
    const results = await screen.findByRole("region", {
      name: "Free provider test",
    })
    expect(results).toHaveTextContent("Answered in 120 ms")
    expect(results).toHaveTextContent("Busy right now (rate limited)")
    expect(results).toHaveTextContent("429 too many requests")
    expect(localStorage.getItem(FREE_TEST_STORAGE_KEY)).not.toBeNull()

    unmount()
    renderWorkspace()
    const kept = await screen.findByRole("region", {
      name: "Free provider test",
    })
    expect(kept).toHaveTextContent("429 too many requests")
    await user.click(
      within(kept).getByRole("button", { name: "Dismiss results" }),
    )
    expect(
      screen.queryByRole("region", { name: "Free provider test" }),
    ).not.toBeInTheDocument()
    expect(localStorage.getItem(FREE_TEST_STORAGE_KEY)).toBeNull()
  })

  it("says why each free provider was not added, in one short line", async () => {
    const long =
      "The provider answered with an error. ".repeat(12).trim() + " Try later."
    freeTestReply = {
      ok: true,
      total: 3,
      catalog_discovered: 2,
      verified: 0,
      instances: [],
      outcomes: [
        {
          registry_id: "empty_free",
          provider_id: "empty-free",
          status: "failed",
          error_class: "no_model",
          error: "It lists no model Compa can add.",
        },
        {
          registry_id: "quiet_free",
          provider_id: "quiet-free",
          status: "connected",
          error: long,
        },
        {
          registry_id: "odd_free",
          provider_id: "odd-free",
          status: "failed",
          error_class: "something_new",
        },
      ],
    }
    const user = userEvent.setup()
    renderWorkspace()
    await user.click(
      await screen.findByRole("button", { name: "Try free providers" }),
    )
    const results = await screen.findByRole("region", {
      name: "Free provider test",
    })
    const [empty, quiet, odd] = within(results).getAllByRole("listitem")
    expect(empty).toHaveTextContent("No free chat model to add right now")
    expect(quiet).toHaveTextContent("Listed models but didn't answer")
    // A reason this version does not know reads as the plain status.
    expect(odd).toHaveTextContent("Couldn't be reached")
    expect(odd).not.toHaveTextContent("models.freeTest")
    const error = within(quiet).getByText(long)
    expect(error).toHaveClass("line-clamp-2")
    expect(error).toHaveAttribute("title", long)
  })

  it("keeps identical model IDs distinct by exact instance target", async () => {
    renderWorkspace()
    await userEvent.click(
      await screen.findByRole("tab", { name: "Models & Routes" }),
    )
    expect(screen.getAllByText("first/shared").length).toBeGreaterThan(0)
    expect(screen.getAllByText("second/shared").length).toBeGreaterThan(0)
    expect(screen.queryByText("gpt-5.4-mini")).not.toBeInTheDocument()
  })

  it("lists only chat models as available, by provider with a count", async () => {
    targets = [
      target("first", "shared", { instance_label: "First Provider" }),
      target("first", "bigger", {
        label: "Bigger",
        instance_label: "First Provider",
        surfaces: ["chat_completions"],
      }),
      ...Array.from({ length: 3 }, (_, index) =>
        target("ext-speech", `demo_voice_${index}`, {
          instance_label: "Example Voice",
          surfaces: ["audio_speech"],
        }),
      ),
      target("second", "shared", { instance_label: "Second Provider" }),
    ]
    renderWorkspace()
    await userEvent.click(
      await screen.findByRole("tab", { name: "Models & Routes" }),
    )
    const first = await screen.findByRole("group", { name: "First Provider" })
    expect(first).toHaveTextContent("2 models")
    expect(
      within(first)
        .getAllByRole("listitem")
        .map((item) => item.textContent),
    ).toEqual(["sharedfirst/shared", "Biggerfirst/bigger"])
    expect(
      screen.getByRole("group", { name: "Second Provider" }),
    ).toHaveTextContent("1 model")
    // Voices serve no chat, so a route cannot use them.
    expect(
      screen.queryByRole("group", { name: "Example Voice" }),
    ).not.toBeInTheDocument()
    expect(screen.queryByText(/demo_voice/)).not.toBeInTheDocument()
    expect(screen.getByText("2 models, tried in order")).toBeInTheDocument()
  })

  it("edits route order using only backend-provided targets", async () => {
    renderWorkspace()
    await userEvent.click(
      await screen.findByRole("tab", { name: "Models & Routes" }),
    )
    await userEvent.click(screen.getByRole("button", { name: "Edit" }))
    expect(await screen.findByText("Edit route")).toBeInTheDocument()
    await waitFor(() =>
      expect(screen.getAllByText("second/shared").length).toBeGreaterThan(1),
    )
  })

  describe("default model", () => {
    it("says when no default model is set", async () => {
      renderWorkspace()
      await waitFor(() =>
        expect(defaultBar()).toHaveTextContent(
          "No default model selected yet.",
        ),
      )
      expect(
        within(defaultBar()).queryByRole("button", { name: "Clear default" }),
      ).not.toBeInTheDocument()
    })

    it("sets a model in Chat as the default and names it", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      await user.click(
        await screen.findByRole("button", {
          name: "Set as default: shared · first",
        }),
      )
      await waitFor(() =>
        expect(
          within(defaultBar()).getByTitle("first/shared"),
        ).toHaveTextContent("shared"),
      )
      expect(defaultBar()).toHaveTextContent("first")
      expect(writes("/api/default-model")).toEqual([
        {
          path: "/api/default-model",
          method: "PUT",
          body: { selection: "first/shared" },
        },
      ])
      expect(
        screen.queryByRole("button", {
          name: "Set as default: shared · first",
        }),
      ).not.toBeInTheDocument()
      expect(screen.getByText("Default")).toBeInTheDocument()
    })

    it("reloads the default after the free provider test", async () => {
      freeTestReply = {
        ok: true,
        total: 1,
        catalog_discovered: 1,
        verified: 1,
        instances: ["first"],
        outcomes: [],
      }
      const user = userEvent.setup()
      renderWorkspace()
      await waitFor(() =>
        expect(defaultBar()).toHaveTextContent(
          "No default model selected yet.",
        ),
      )
      // The server makes the first model added to Chat the default.
      defaultSelection = "first/shared"
      await user.click(
        screen.getByRole("button", { name: "Try free providers" }),
      )
      await waitFor(() =>
        expect(within(defaultBar()).getByTitle("first/shared")).toBeVisible(),
      )
    })

    it("sets a route as the default", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      await user.click(
        await screen.findByRole("tab", { name: "Models & Routes" }),
      )
      await user.click(
        await screen.findByRole("button", {
          name: "Set as default: primary",
        }),
      )
      await waitFor(() => expect(defaultBar()).toHaveTextContent("primary"))
      expect(writes("/api/default-model").at(-1)?.body).toEqual({
        selection: "primary",
      })
      expect(screen.getByText("Default")).toBeInTheDocument()
    })

    it("clears the default model", async () => {
      defaultSelection = "primary"
      const user = userEvent.setup()
      renderWorkspace()
      await waitFor(() => expect(defaultBar()).toHaveTextContent("primary"))
      await user.click(
        within(defaultBar()).getByRole("button", { name: "Clear default" }),
      )
      await waitFor(() =>
        expect(defaultBar()).toHaveTextContent(
          "No default model selected yet.",
        ),
      )
      expect(writes("/api/default-model").at(-1)?.body).toEqual({
        selection: "",
      })
    })
  })

  describe("instance runtime settings", () => {
    const openEditDialog = async (
      user: ReturnType<typeof userEvent.setup>,
      id: string,
    ) => {
      await user.click(
        await screen.findByRole("button", { name: `Manage ${id}` }),
      )
      await user.click(screen.getByRole("button", { name: "Edit" }))
      return screen.findByRole("dialog", { name: `Edit ${id}` })
    }

    it("saves every runtime field from the advanced section", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      const dialog = await openEditDialog(user, "first")
      const scope = within(dialog)
      expect(scope.queryByLabelText("Proxy")).not.toBeInTheDocument()
      await user.click(scope.getByRole("button", { name: /Advanced options/ }))

      await user.click(scope.getByRole("combobox", { name: "Thinking level" }))
      await user.click(
        await screen.findByRole("option", { name: "Extra high" }),
      )
      // Streaming is on until it is turned off.
      const streaming = scope.getByRole("switch", { name: "Streaming output" })
      expect(streaming).toBeChecked()
      await user.click(streaming)
      expect(streaming).not.toBeChecked()
      await user.type(scope.getByLabelText("Request timeout (s)"), "45")
      await user.type(scope.getByLabelText("Rate limit (RPM)"), "20")
      await user.type(scope.getByLabelText("Proxy"), "socks5://127.0.0.1:1080")
      await user.type(
        scope.getByLabelText("Max tokens field"),
        "max_completion_tokens",
      )
      await user.type(scope.getByLabelText("Tool schema transform"), "simple")
      await user.click(scope.getByLabelText("Extra body"))
      await user.paste('{"reasoning_split": true}')
      await user.click(scope.getByRole("button", { name: "Save changes" }))

      await waitFor(() =>
        expect(writes("/api/provider-instances/first")).toHaveLength(1),
      )
      expect(writes("/api/provider-instances/first")[0]).toMatchObject({
        method: "PUT",
        body: {
          id: "first",
          runtime: {
            proxy: "socks5://127.0.0.1:1080",
            request_timeout: 45,
            rpm: 20,
            streaming: false,
            thinking_level: "xhigh",
            max_tokens_field: "max_completion_tokens",
            tool_schema_transform: "simple",
            extra_body: { reasoning_split: true },
          },
        },
      })
      await waitFor(() =>
        expect(screen.queryByRole("dialog")).not.toBeInTheDocument(),
      )
    })

    it("blocks the save on invalid extra body JSON and shows why", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      const dialog = await openEditDialog(user, "first")
      const scope = within(dialog)
      await user.click(scope.getByRole("button", { name: /Advanced options/ }))
      await user.click(scope.getByLabelText("Extra body"))
      await user.paste('{"reasoning_split": ')
      await user.click(scope.getByRole("button", { name: "Save changes" }))

      expect(await scope.findByText("Invalid JSON format")).toBeInTheDocument()
      expect(scope.getByLabelText("Extra body")).toHaveAttribute(
        "aria-invalid",
        "true",
      )
      expect(scope.getByRole("alert")).toHaveTextContent(
        "Fix the highlighted advanced settings before saving.",
      )
      expect(writes("/api/provider-instances/first")).toHaveLength(0)

      await user.clear(scope.getByLabelText("Extra body"))
      await user.paste("[1]")
      expect(await scope.findByText(/Enter a JSON object/)).toBeInTheDocument()
      await user.clear(scope.getByLabelText("Extra body"))
      await user.click(scope.getByRole("button", { name: "Save changes" }))
      await waitFor(() =>
        expect(writes("/api/provider-instances/first")).toHaveLength(1),
      )
      expect(writes("/api/provider-instances/first")[0].body).toMatchObject({
        runtime: {},
      })
    })

    it("prefills stored runtime settings and keeps them on save", async () => {
      const user = userEvent.setup()
      renderWorkspace()
      const dialog = await openEditDialog(user, "second")
      const scope = within(dialog)
      const toggle = scope.getByRole("button", { name: /Advanced options/ })
      expect(toggle).toHaveTextContent("2 settings")
      await user.click(toggle)
      expect(scope.getByLabelText("Rate limit (RPM)")).toHaveValue(30)
      expect(scope.getByLabelText("Extra body")).toHaveValue(
        '{\n  "reasoning_split": true\n}',
      )
      // Unset streaming reads as on, and saving leaves it unset.
      expect(
        scope.getByRole("switch", { name: "Streaming output" }),
      ).toBeChecked()
      await user.click(scope.getByRole("button", { name: "Save changes" }))
      await waitFor(() =>
        expect(writes("/api/provider-instances/second")).toHaveLength(1),
      )
      expect(writes("/api/provider-instances/second")[0].body).toEqual(
        expect.objectContaining({
          runtime: { rpm: 30, extra_body: { reasoning_split: true } },
        }),
      )
    })

    it("shows streaming turned off as off", async () => {
      instances = [instance("first", { streaming: false })]
      const user = userEvent.setup()
      renderWorkspace()
      const dialog = await openEditDialog(user, "first")
      const scope = within(dialog)
      await user.click(scope.getByRole("button", { name: /Advanced options/ }))
      expect(
        scope.getByRole("switch", { name: "Streaming output" }),
      ).not.toBeChecked()
      await user.click(scope.getByRole("button", { name: "Save changes" }))
      await waitFor(() =>
        expect(writes("/api/provider-instances/first")).toHaveLength(1),
      )
      expect(writes("/api/provider-instances/first")[0].body).toEqual(
        expect.objectContaining({ runtime: { streaming: false } }),
      )
    })
  })
})
