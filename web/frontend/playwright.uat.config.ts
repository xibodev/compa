import { defineConfig, devices } from "@playwright/test"

const baseURL = process.env.COMPA_UAT_BASE_URL
if (!baseURL) throw new Error("COMPA_UAT_BASE_URL is required")
const fakeAudioArg = process.env.COMPA_UAT_AUDIO_FILE
  ? [`--use-file-for-fake-audio-capture=${process.env.COMPA_UAT_AUDIO_FILE}`]
  : []

export default defineConfig({
  testDir: "./tests/uat",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 180_000,
  use: {
    baseURL,
    locale: "en-US",
    timezoneId: "UTC",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "retain-on-failure",
  },
  projects: [
    {
      name: "desktop-chromium",
      use: {
        ...devices["Desktop Chrome"],
        launchOptions: {
          args: [
            "--use-fake-ui-for-media-stream",
            "--use-fake-device-for-media-stream",
            ...fakeAudioArg,
          ],
        },
      },
    },
    {
      name: "mobile-chromium",
      use: {
        ...devices["Pixel 7"],
        launchOptions: {
          args: [
            "--use-fake-ui-for-media-stream",
            "--use-fake-device-for-media-stream",
            ...fakeAudioArg,
          ],
        },
      },
    },
  ],
})
