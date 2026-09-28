import { type Page, expect } from "@playwright/test"

/**
 * Signs in and waits for the gateway. On a fresh first run it sets the
 * password, which signs in at once: there is no second password prompt.
 */
export async function authenticate(page: Page, password: string) {
  await page.addInitScript(() => {
    localStorage.setItem(
      "compa-tour-state",
      JSON.stringify({ currentStep: "completed", isActive: false }),
    )
  })
  await page.goto("/")
  await expect(page).toHaveURL(/\/(launcher-setup|launcher-login)$/)
  // Both first-run pages show the product.
  await expect(page.getByText("Compa", { exact: true })).toBeVisible()
  if (page.url().endsWith("/launcher-setup")) {
    await page.locator("#setup-password").fill(password)
    await page.locator("#setup-confirm").fill(password)
    await page.getByRole("button", { name: /set password/i }).click()
  } else {
    await page.locator("#launcher-password").fill(password)
    await page.getByRole("button", { name: /sign in/i }).click()
  }
  await expect(page).toHaveURL(/\/$/)
  await expect
    .poll(
      async () => {
        const response = await page.request.get("/api/gateway/status")
        return (await response.json()).gateway_status
      },
      { timeout: 30_000 },
    )
    .toBe("running")
}
