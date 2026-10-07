import fs from "node:fs/promises"
import path from "node:path"

import { type Page, expect, test } from "@playwright/test"

import { authenticate as signIn } from "./helpers"

const password = process.env.COMPA_UAT_PASSWORD
const fakeURL = process.env.COMPA_UAT_FAKE_URL
const workspace = process.env.COMPA_UAT_WORKSPACE
if (!password || !fakeURL || !workspace) {
  throw new Error("UAT environment is incomplete")
}

const authenticate = (page: Page) => signIn(page, password)

// The roster entry every OpenAI-compatible endpoint is added through.
const CUSTOM_PROVIDER = "Custom OpenAI-compatible"

async function configureProviderThroughUI(page: Page) {
  await page.goto("/models")
  await expect(
    page.getByRole("heading", { name: "Providers", exact: true }),
  ).toBeVisible()
  // The provider list is the one place to add a provider: no separate
  // API-key cards and no free-standing Custom button.
  await expect(page.getByRole("group", { name: /connection$/ })).toHaveCount(0)
  await expect(
    page.getByRole("button", { name: "Custom", exact: true }),
  ).toHaveCount(0)
  await expect(page.getByRole("link", { name: /Credentials/i })).toHaveCount(0)
  const instanceResponse = await page.request.get("/api/provider-instances")
  const instanceData = await instanceResponse.json()
  const providerExists = instanceData.instances?.some(
    (instance: { id?: string }) => instance.id === "uat-provider",
  )
  const syncResponse = () =>
    page.waitForResponse(
      (response) =>
        response
          .url()
          .includes("/provider-instances/uat-provider/catalog/sync") &&
        response.request().method() === "POST",
    )
  if (!providerExists) {
    await page
      .getByRole("button", { name: `Connect ${CUSTOM_PROVIDER}` })
      .click()
    const dialog = page.getByRole("dialog", {
      name: `Connect ${CUSTOM_PROVIDER}`,
    })
    await dialog.getByLabel("Connection name").fill("uat-provider")
    await dialog.getByLabel("API address").fill(`${fakeURL}/v1`)
    // A new connection loads its models on its own.
    const synced = syncResponse()
    await dialog.getByRole("button", { name: "Connect provider" }).click()
    expect((await synced).ok()).toBe(true)
    await expect(dialog).toBeHidden()
  } else {
    await page
      .getByRole("button", { name: `Manage ${CUSTOM_PROVIDER}` })
      .click()
    const synced = syncResponse()
    await page.getByRole("button", { name: "Refresh models" }).click()
    expect((await synced).ok()).toBe(true)
  }

  const inspector = page.getByRole("region", { name: CUSTOM_PROVIDER })
  await expect(inspector.getByText("Models (1)")).toBeVisible({
    timeout: 15_000,
  })
  // A keyless connection is listed with the free providers, as connected.
  await expect(
    page
      .getByRole("region", { name: "Free — no key needed" })
      .getByRole("button", { name: `Manage ${CUSTOM_PROVIDER}` }),
  ).toContainText("Connected")
  const addToChat = inspector.getByRole("button", { name: "Add to Chat" })
  if ((await addToChat.count()) > 0) await addToChat.click()
  await expect(inspector.getByRole("button", { name: "In Chat" })).toBeVisible()
  // The first model added to Chat becomes the default, and the bar says so.
  await expect(
    page.getByRole("region", { name: "Default model" }),
  ).toContainText("uat-model")
}

async function selectUATModel(page: Page) {
  await page.goto("/")
  const selector = page.getByRole("combobox", { name: "Chat model" })
  await selector.click()
  await page.getByRole("option", { name: "uat-model", exact: true }).click()
  // The model reads by name; the exact target is its tooltip.
  await expect(selector).toContainText("uat-model")
  await expect(selector).toHaveAttribute("title", "uat-provider/uat-model")
  await expect(page.getByPlaceholder("Start a new message…")).toBeEnabled()
}

async function send(page: Page, prompt: string, expected: string) {
  const composer = page.getByPlaceholder("Start a new message…")
  await composer.fill(prompt)
  await page.getByRole("button", { name: "Send message" }).click()
  await expect(page.getByText(expected, { exact: true }).last()).toBeVisible({
    timeout: 30_000,
  })
}

test("human-like core operator journey", async ({ page, request }) => {
  await request.post(`${fakeURL}/__uat/reset`)
  const browserErrors: string[] = []
  page.on("pageerror", (error) => browserErrors.push(error.message))
  page.on("console", (message) => {
    if (message.type() === "error") browserErrors.push(message.text())
  })

  // 1. Fresh first run.
  const navigationStarted = Date.now()
  await authenticate(page)
  expect(Date.now() - navigationStarted).toBeLessThan(15_000)
  await expect(page.getByRole("combobox", { name: "Chat model" })).toBeVisible()

  // 2. Configure an anonymous OpenAI-compatible provider entirely through UI.
  await configureProviderThroughUI(page)

  // The default model the first added model became applies at once: Chat
  // answers on it without a gateway restart.
  const applied = await (await page.request.get("/api/gateway/status")).json()
  expect(applied.gateway_status).toBe("running")
  expect(applied.gateway_restart_required).toBe(false)
  await page.goto("/")
  await expect(
    page.getByRole("combobox", { name: "Chat model" }),
  ).toContainText("(Default)")
  await send(page, "UAT_DEFAULT_MODEL", "UAT_ASSISTANT_REPLY")
  // The rest of the journey starts in a chat of its own.
  await page.getByRole("button", { name: "New Chat" }).click()

  // Model selection is configuration state and must remain available while the
  // gateway is stopped; only sending a message depends on the runtime.
  const stopResponse = await page.request.post("/api/gateway/stop")
  expect(stopResponse.ok()).toBe(true)
  await expect
    .poll(async () => {
      const response = await page.request.get("/api/gateway/status")
      return (await response.json()).gateway_status
    })
    .toBe("stopped")
  await page.goto("/")
  const stoppedSelector = page.getByRole("combobox", { name: "Chat model" })
  await stoppedSelector.click()
  await expect(
    page.getByRole("option", { name: "uat-model", exact: true }),
  ).toBeVisible()
  await page.keyboard.press("Escape")
  // The gateway's state is a labelled status; starting it is in its menu.
  await page.getByRole("button", { name: "Gateway: Stopped" }).click()
  await page.getByRole("menuitem", { name: "Start gateway" }).click()
  await expect
    .poll(
      async () => {
        const response = await page.request.get("/api/gateway/status")
        return (await response.json()).gateway_status
      },
      { timeout: 30_000 },
    )
    .toBe("running")
  await selectUATModel(page)

  // 3. Multi-turn context.
  await send(page, "UAT_TURN_1", "UAT_TURN_1_REPLY")
  await expect
    .poll(async () => {
      const response = await page.request.get("/api/sessions?offset=0&limit=20")
      const sessions = await response.json()
      return sessions.some(
        (session: { preview?: string; message_count?: number }) =>
          session.preview === "UAT_TURN_1" && (session.message_count ?? 0) >= 2,
      )
    })
    .toBe(true)

  // Voice transcription is a real Chat turn, not only text in the composer.
  await page.getByRole("button", { name: "Speak to agent" }).click()
  await page.waitForTimeout(2_500)
  await page
    .getByRole("button", { name: "Stop recording & transcribe" })
    .click()
  await expect(page.getByText("UAT_VOICE_PROMPT", { exact: true })).toBeVisible(
    {
      timeout: 30_000,
    },
  )
  await expect(
    page.getByText("UAT_ASSISTANT_REPLY", { exact: true }).last(),
  ).toBeVisible({ timeout: 30_000 })

  // Hands-free uses VAD, then the same durable selected-agent Chat path.
  const voiceMessageCountBefore = await page
    .getByText("UAT_VOICE_PROMPT", { exact: true })
    .count()
  const voiceReplyCountBefore = await page
    .getByText("UAT_ASSISTANT_REPLY", { exact: true })
    .count()
  await page.getByRole("button", { name: "Push-to-talk" }).click()
  await page.getByRole("button", { name: "Start hands-free" }).click()
  // The fake microphone speaks at once, so the loop may already be past
  // listening; any active state shows that hands-free started.
  await expect(
    page.getByText(
      /Hands-free Voice · (listening|transcribing|waiting|speaking)/,
    ),
  ).toBeVisible()
  await expect(page.getByText("UAT_VOICE_PROMPT", { exact: true })).toHaveCount(
    voiceMessageCountBefore + 1,
    { timeout: 30_000 },
  )
  await expect(
    page.getByText("UAT_ASSISTANT_REPLY", { exact: true }),
  ).toHaveCount(voiceReplyCountBefore + 1, { timeout: 30_000 })
  await expect
    .poll(async () => {
      const response = await request.get(`${fakeURL}/__uat/requests`)
      return (await response.json()).tts.length
    })
    .toBeGreaterThan(0)
  await expect(page.getByRole("button", { name: /Stop ·/ })).toBeVisible()
  await page.getByRole("button", { name: "End call" }).click({ force: true })
  await expect(page.getByText(/Hands-free Voice ·/)).toHaveCount(0)

  await send(page, "UAT_TURN_2", "UAT_CONTEXT_OK")

  // 4. Real write_file and read_file execution with disk proof.
  await send(page, "UAT_TOOL_PROMPT", "UAT_TOOL_COMPLETE")
  await expect(page.getByText(/Tool calls: write_file/)).toBeVisible()
  await expect(page.getByText(/Tool calls: read_file/)).toBeVisible()
  await expect
    .poll(() => fs.readFile(path.join(workspace, "uat-tool.txt"), "utf8"))
    .toBe("UAT_TOOL_CONTENT")

  // 5. New Chat and restoration from History.
  const selectedBeforeNewChat = await page
    .getByRole("combobox", { name: "Chat model" })
    .textContent()
  await page.getByRole("button", { name: "New Chat" }).click()
  await expect(page.getByText("UAT_TURN_1", { exact: true })).toHaveCount(0)
  await expect(
    page.getByRole("combobox", { name: "Chat model" }),
  ).toContainText(selectedBeforeNewChat?.trim() || "uat-provider/uat-model")
  await send(page, "UAT_NEW_SESSION", "UAT_ASSISTANT_REPLY")
  await page.getByRole("button", { name: "History" }).click()
  const previous = page
    .getByRole("menuitem")
    .filter({ hasText: "UAT_TURN_1" })
    .first()
  await expect(previous).toBeVisible()
  await previous.click()
  const main = page.getByRole("main")
  await expect(main.getByText("UAT_TURN_1", { exact: true })).toBeVisible()
  await expect(main.getByText("UAT_CONTEXT_OK", { exact: true })).toBeVisible()

  // 6. Provider failure and recovery.
  await request.post(`${fakeURL}/__uat/fail-chat`)
  const composer = page.getByPlaceholder("Start a new message…")
  await composer.fill("UAT_FAILURE")
  await page.getByRole("button", { name: "Send message" }).click()
  await expect(
    page.getByText(/UAT_PROVIDER_DOWN|Error processing message/),
  ).toBeVisible({
    timeout: 30_000,
  })
  await request.post(`${fakeURL}/__uat/recover-chat`)
  await send(page, "UAT_RECOVERED", "UAT_ASSISTANT_REPLY")

  // 7. This same journey runs in desktop and mobile Playwright projects.
  const fakeState = await request.get(`${fakeURL}/__uat/requests`)
  const observed = await fakeState.json()
  expect(
    observed.chat.some((call: { messages: Array<{ content?: string }> }) =>
      call.messages.some((message) => message.content === "UAT_TURN_1"),
    ),
  ).toBe(true)
  expect(observed.stt).toBeGreaterThan(0)
  expect(observed.tts.length).toBeGreaterThan(0)
  expect(
    observed.chat.some((call: { messages: Array<{ content?: string }> }) =>
      call.messages.some((message) => message.content === "UAT_VOICE_PROMPT"),
    ),
  ).toBe(true)
  const submittedVoiceTurns = observed.chat.filter(
    (call: { messages: Array<{ role?: string; content?: string }> }) =>
      [...call.messages].reverse().find((message) => message.role === "user")
        ?.content === "UAT_VOICE_PROMPT",
  )
  expect(submittedVoiceTurns).toHaveLength(2)
  expect(browserErrors).toEqual([])
})
