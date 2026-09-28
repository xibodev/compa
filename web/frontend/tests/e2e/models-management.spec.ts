import { type Page, expect, test } from "@playwright/test"

test.beforeEach(async ({ page, request }) => {
  await request.post("/__fixture/reset")
  await page.addInitScript(() => {
    localStorage.setItem(
      "compa-tour-state",
      JSON.stringify({ currentStep: "completed", isActive: false }),
    )
  })
})

const expectNoHorizontalScroll = async (page: Page) =>
  expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollWidth <=
          document.documentElement.clientWidth,
      ),
    )
    .toBe(true)

test("management is responsive and instance-owned", async ({ page }) => {
  await page.goto("/models")
  await expect(
    page.getByRole("heading", { name: "Providers", exact: true }),
  ).toBeVisible()
  await expect(page.getByText("Mystery protocol")).toBeVisible()
  await expect(
    page.getByRole("button", { name: "Configure", exact: true }),
  ).toHaveCount(0)
  // The provider list is the one place to add a provider.
  await expect(page.getByRole("group", { name: /connection$/ })).toHaveCount(0)
  await expect(
    page.getByRole("button", { name: "Custom", exact: true }),
  ).toHaveCount(0)
  await expect(page.getByRole("tab")).toHaveText([
    "Providers",
    "Models & Routes",
  ])
  await expectNoHorizontalScroll(page)
  await page.getByRole("tab", { name: "Models & Routes" }).click()
  await expect(page.getByText("first/shared").first()).toBeVisible()
  await expect(page.getByText("second/shared").first()).toBeVisible()
  await expect(page.getByText("disabled/shared")).toHaveCount(0)
  await expect(page.getByText("gpt-5.4-mini")).toHaveCount(0)
})

test("statuses say what a connection can do", async ({ page }) => {
  await page.goto("/models")
  const gamma = page.getByRole("button", { name: "Manage Gamma Account" })
  await expect(gamma).toContainText("Needs sign-in")
  await expect(page.getByText("ext-gamma")).toHaveCount(0)
  await expect(
    page.getByRole("button", { name: "Manage disabled" }),
  ).toContainText("Disabled")
  // A provider that still needs its sign-in is not counted as connected.
  await expect(
    page.getByRole("button", { name: "Connected (2)" }),
  ).toBeVisible()
  await gamma.click()
  const inspector = page.getByRole("region", { name: "Gamma Account" })
  // The extension owns this provider's address and sign-in: its card
  // offers neither an edit, a test nor a removal, and names no address.
  await expect(inspector).toContainText("Served by the extension")
  await expect(inspector).not.toContainText("127.0.0.1")
  await expect(inspector).not.toContainText("chat_completions")
  // The fixture's extension is not connected, so there is no sign-in yet.
  await expect(inspector).toContainText("Sign-in needs the extension.")
  for (const name of [
    /Go to Extension/,
    /Edit/,
    /Test connection/,
    /Refresh models/,
    /Remove/,
    /Add another connection/,
  ])
    await expect(inspector.getByRole("button", { name })).toHaveCount(0)
})

test("free provider results survive a reload", async ({ page }) => {
  await page.goto("/models")
  await page.getByRole("button", { name: "Try free providers" }).click()
  const results = page.getByRole("region", { name: "Free provider test" })
  await expect(results).toContainText("Busy right now (rate limited)")
  await expect(results).toContainText("429 Too Many Requests")
  await page.reload()
  await expect(results).toContainText("429 Too Many Requests")
  await results.getByRole("button", { name: "Dismiss results" }).click()
  await expect(results).toHaveCount(0)
})

test("catalog refresh sends an empty body", async ({ page }) => {
  let body = ""
  page.on("request", (request) => {
    if (request.url().endsWith("/catalog/sync")) body = request.postData() || ""
  })
  await page.goto("/models")
  await page.getByRole("button", { name: "Manage first" }).click()
  await page.getByRole("button", { name: "Refresh models" }).click()
  await expect.poll(() => body).toBe("{}")
})

test("route order persists across reload", async ({ page }) => {
  await page.goto("/models")
  await page.getByRole("tab", { name: "Models & Routes" }).click()
  await page.getByRole("button", { name: "Edit" }).click()
  await page.getByRole("button", { name: "Up" }).last().click()
  await page.getByRole("button", { name: "Save route" }).click()
  await expect(page.getByRole("dialog")).toHaveCount(0)
  await page.reload()
  await page.getByRole("tab", { name: "Models & Routes" }).click()
  await page.getByRole("button", { name: "Edit" }).click()
  await expect(page.getByRole("dialog").getByRole("listitem")).toHaveText([
    /first\/shared/,
    /second\/shared/,
  ])
})

test("the default model is set from a route or an active target", async ({
  page,
}) => {
  const defaultModel = page.getByRole("region", { name: "Default model" })
  await page.goto("/models")
  await expect(defaultModel).toContainText("No default model selected yet.")

  await page
    .getByRole("button", { name: "Set as default: shared · first" })
    .click()
  await expect(defaultModel.getByTitle("first/shared")).toContainText("shared")

  await page.getByRole("tab", { name: "Models & Routes" }).click()
  await page.getByRole("button", { name: "Set as default: primary" }).click()
  await expect(defaultModel).toContainText("primary")
  await page.reload()
  await expect(defaultModel).toContainText("primary")

  await defaultModel.getByRole("button", { name: "Clear default" }).click()
  await expect(defaultModel).toContainText("No default model selected yet.")
})

test("instance runtime settings persist across reload", async ({ page }) => {
  const openEditor = async () => {
    await page.getByRole("button", { name: "Manage first" }).click()
    await page.getByRole("button", { name: "Edit", exact: true }).click()
    return page.getByRole("dialog", { name: "Edit first" })
  }
  await page.goto("/models")
  let dialog = await openEditor()
  await dialog.getByRole("button", { name: "Advanced options" }).click()
  await dialog.getByRole("combobox", { name: "Thinking level" }).click()
  await page.getByRole("option", { name: "High", exact: true }).click()
  await dialog.getByLabel("Rate limit (RPM)").fill("30")
  await dialog.getByLabel("Extra body").fill("[1]")
  await dialog.getByRole("button", { name: "Save changes" }).click()
  await expect(dialog.getByText(/Enter a JSON object/)).toBeVisible()
  await dialog.getByLabel("Extra body").fill('{"reasoning_split": true}')
  await dialog.getByRole("button", { name: "Save changes" }).click()
  await expect(dialog).toHaveCount(0)

  await page.reload()
  dialog = await openEditor()
  const advanced = dialog.getByRole("button", { name: /Advanced options/ })
  await expect(advanced).toContainText("3 settings")
  await advanced.click()
  await expect(
    dialog.getByRole("combobox", { name: "Thinking level" }),
  ).toHaveText("High")
  await expect(dialog.getByLabel("Rate limit (RPM)")).toHaveValue("30")
  await expect(dialog.getByLabel("Extra body")).toHaveValue(
    '{\n  "reasoning_split": true\n}',
  )
})

test("the shell names its controls and routes", async ({ page, isMobile }) => {
  await page.goto("/models")
  await expect(page.locator("html")).toHaveAttribute("lang", "en")
  await expect(
    page.getByRole("button", { name: "Gateway: Stopped" }),
  ).toBeVisible()
  await expect(
    page.getByRole("button", { name: "Change language" }),
  ).toBeVisible()
  await expect(
    page.getByRole("button", { name: /Switch to (light|dark) theme/ }),
  ).toBeVisible()
  if (isMobile)
    await page.getByRole("button", { name: "Toggle Sidebar" }).first().click()
  const nav = page.getByRole("navigation", { name: "Main navigation" })
  await expect(nav.getByRole("link", { name: "Channels" })).toBeVisible()
  await expect(nav.getByRole("link", { name: "Modules" })).toHaveAttribute(
    "href",
    "/agent/modules",
  )
  await expect(nav.getByRole("link", { name: "Voice" })).toHaveAttribute(
    "href",
    "/config/voice",
  )
  await expect(nav.getByRole("link", { name: "Telegram" })).toHaveCount(0)
})

test("channels live on one page", async ({ page }) => {
  await page.goto("/channels")
  await expect(page.getByRole("link", { name: /Telegram/ })).toHaveAttribute(
    "href",
    "/channels/telegram",
  )
  // The browser chat is not a chat app channel: turning it off here would
  // cut off this very chat.
  await expect(page.getByRole("link", { name: /Web chat/ })).toHaveCount(0)
  await expectNoHorizontalScroll(page)
})

test("an unknown address gets a real 404 page", async ({ page }) => {
  await page.goto("/nowhere")
  await expect(
    page.getByRole("heading", { name: "Page not found" }),
  ).toBeVisible()
  await expect(
    page.getByRole("link", { name: "Back to Chat" }),
  ).toHaveAttribute("href", "/")
})
