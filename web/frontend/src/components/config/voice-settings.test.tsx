import { fireEvent, render, screen, waitFor } from "@testing-library/react"
import userEvent from "@testing-library/user-event"
import { afterEach, describe, expect, it, vi } from "vitest"

import type { VoiceConfig } from "@/features/voice/voice-client"
import "@/i18n"

import { VoiceSettings } from "./voice-settings"

const response = (value: unknown) =>
  Promise.resolve(new Response(JSON.stringify(value), { status: 200 }))

const OPTIONS = {
  stt: [
    {
      target: "el/scribe_v2",
      label: "scribe_v2",
      provider_kind: "elevenlabs",
    },
  ],
  tts: [
    {
      target: "el/eleven_v3",
      label: "eleven_v3",
      provider_kind: "elevenlabs",
    },
    {
      target: "ext-speech/es-MX-DaliaNeural",
      label: "es-MX-DaliaNeural",
      provider_kind: "extension",
    },
    {
      target: "ext-speech/en-US-AriaNeural",
      label: "en-US-AriaNeural",
      provider_kind: "extension",
    },
  ],
  stt_chat: [
    {
      target: "oa/gpt-4o-audio",
      label: "gpt-4o-audio",
      provider_kind: "openai",
    },
  ],
}

// stubVoiceAPI serves config as the saved voice config and records the
// bodies of voice config updates.
function stubVoiceAPI(
  config: Partial<VoiceConfig>,
  options: typeof OPTIONS = OPTIONS,
) {
  const updates: unknown[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input)
      if (path === "/api/voice/config" && init?.method === "PUT") {
        const body = JSON.parse(String(init.body))
        updates.push(body)
        return response({ ok: true, config: body })
      }
      if (path === "/api/voice/config") return response({ ok: true, config })
      if (path === "/api/voice/options") return response(options)
      if (path === "/api/provider-targets?all=true")
        return response({
          targets: [
            {
              target: "ext-speech/es-MX-DaliaNeural",
              instance_id: "ext-speech",
              model_id: "es-MX-DaliaNeural",
              provider_kind: "extension",
              instance_label: "Speech Service",
              fetched_at: "",
            },
          ],
        })
      if (path === "/api/provider-instances")
        return response({
          instances: [
            {
              id: "ext-speech",
              provider_kind: "extension",
              adapter: "extension",
              protocol: "audio",
              auth_configured: false,
              header_names: [],
              setting_names: [],
              state: "enabled",
              display_name: "Speech Service",
            },
          ],
        })
      return Promise.resolve(new Response("missing", { status: 404 }))
    }),
  )
  return updates
}

// The pickers open a popper-positioned Radix popover, which user-event
// drives many seconds per step under jsdom, so they are driven with events.
describe("VoiceSettings", () => {
  afterEach(() => vi.unstubAllGlobals())

  it("offers the provider-derived voice targets", async () => {
    stubVoiceAPI({ enabled: false, mode: "cascade" })

    render(<VoiceSettings />)
    const picker = await screen.findByRole("combobox", {
      name: "Speech-to-text model",
    })

    fireEvent.click(picker)
    expect(
      await screen.findByRole("option", { name: /scribe_v2/ }),
    ).toBeInTheDocument()
    expect(screen.getByRole("option", { name: "None" })).toBeInTheDocument()
    // Typing a model by hand is named plainly.
    expect(
      screen.getByRole("option", { name: "Enter a model ID…" }),
    ).toBeInTheDocument()
    expect(screen.queryByRole("option", { name: /target/i })).toBeNull()
  })

  it("groups voices by language, names their provider and filters as you type", async () => {
    stubVoiceAPI({ enabled: true, mode: "cascade" })

    render(<VoiceSettings />)
    fireEvent.click(
      await screen.findByRole("combobox", { name: "Text-to-speech model" }),
    )
    const spanish = await screen.findByRole("group", {
      name: /Spanish \(Mexico\)/,
    })
    // The provider's name, never the generic kind of its instance.
    await waitFor(() => expect(spanish).toHaveTextContent("Speech Service"))
    expect(spanish).not.toHaveTextContent("extension")

    fireEvent.change(
      screen.getByRole("searchbox", { name: "Search voices or languages" }),
      { target: { value: "spanish" } },
    )
    expect(
      screen.getByRole("option", { name: /es-MX-DaliaNeural/ }),
    ).toBeInTheDocument()
    expect(
      screen.queryByRole("option", { name: /en-US-AriaNeural/ }),
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole("option", { name: /eleven_v3/ }),
    ).not.toBeInTheDocument()
    // None and Enter a model ID stay reachable while filtering.
    expect(screen.getByRole("option", { name: "None" })).toBeInTheDocument()
  })

  it("saves only the voice settings, with the voice name under Advanced", async () => {
    const updates = stubVoiceAPI({
      enabled: true,
      mode: "live",
      stt_target: "el/scribe_v2",
      tts_target: "el/eleven_v3",
      tts_voice: "",
      echo_transcription: false,
    })

    render(<VoiceSettings />)
    await waitFor(() =>
      expect(
        screen.getByRole("combobox", { name: "Speech-to-text model" }),
      ).toHaveTextContent("scribe_v2"),
    )
    await userEvent.click(
      screen.getByRole("switch", { name: "Echo transcriptions" }),
    )
    // The voice name is for providers that ask for one, so it waits under
    // the advanced options.
    expect(
      screen.queryByRole("textbox", { name: "Voice name" }),
    ).not.toBeInTheDocument()
    await userEvent.click(
      screen.getByRole("button", { name: /Advanced options/ }),
    )
    await userEvent.type(
      screen.getByRole("textbox", { name: "Voice name" }),
      "English-US.Male-1",
    )
    await userEvent.click(
      screen.getByRole("button", { name: "Save voice settings" }),
    )

    await waitFor(() => expect(updates).toHaveLength(1))
    expect(updates[0]).toEqual({
      enabled: true,
      mode: "live",
      stt_target: "el/scribe_v2",
      stt_via_chat: false,
      tts_target: "el/eleven_v3",
      tts_voice: "English-US.Male-1",
      echo_transcription: true,
    })
  })

  it("saves spoken replies without dictation, as push-to-talk", async () => {
    const updates = stubVoiceAPI({
      enabled: true,
      mode: "live",
      stt_target: "",
      tts_target: "el/eleven_v3",
    })

    render(<VoiceSettings />)
    expect(
      await screen.findByText(/Chat offers spoken replies only/),
    ).toBeInTheDocument()
    await userEvent.click(
      screen.getByRole("button", { name: "Save voice settings" }),
    )
    await waitFor(() => expect(updates).toHaveLength(1))
    expect(updates[0]).toMatchObject({
      enabled: true,
      mode: "cascade",
      stt_target: "",
      tts_target: "el/eleven_v3",
    })
  })

  it("takes a target that no catalog lists", async () => {
    const updates = stubVoiceAPI({ enabled: false, mode: "cascade" })

    render(<VoiceSettings />)
    fireEvent.click(
      await screen.findByRole("combobox", { name: "Text-to-speech model" }),
    )
    fireEvent.click(
      await screen.findByRole("option", { name: "Enter a model ID…" }),
    )
    await userEvent.type(
      screen.getByRole("textbox", { name: "Text-to-speech model ID" }),
      "local/kokoro",
    )
    await userEvent.click(
      screen.getByRole("button", { name: "Save voice settings" }),
    )

    await waitFor(() => expect(updates).toHaveLength(1))
    expect(updates[0]).toMatchObject({
      stt_target: "",
      tts_target: "local/kokoro",
    })
  })

  it("transcribes with a chat model only when opted in", async () => {
    const updates = stubVoiceAPI({ enabled: false, mode: "cascade" })

    render(<VoiceSettings />)
    const picker = await screen.findByRole("combobox", {
      name: "Speech-to-text model",
    })
    fireEvent.click(picker)
    expect(
      screen.queryByRole("option", { name: /gpt-4o-audio/ }),
    ).not.toBeInTheDocument()
    fireEvent.keyDown(document.activeElement ?? document.body, {
      key: "Escape",
    })
    await waitFor(() =>
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument(),
    )

    await userEvent.click(
      screen.getByRole("switch", { name: "Transcribe with a chat model" }),
    )
    fireEvent.click(
      screen.getByRole("combobox", { name: "Speech-to-text model" }),
    )
    expect(
      screen.queryByRole("option", { name: /scribe_v2/ }),
    ).not.toBeInTheDocument()
    fireEvent.click(await screen.findByRole("option", { name: /gpt-4o-audio/ }))
    await userEvent.click(
      screen.getByRole("button", { name: "Save voice settings" }),
    )

    await waitFor(() => expect(updates).toHaveLength(1))
    expect(updates[0]).toMatchObject({
      stt_target: "oa/gpt-4o-audio",
      stt_via_chat: true,
    })
  })

  it("says so when no connected model accepts audio", async () => {
    stubVoiceAPI(
      { enabled: true, mode: "cascade", stt_via_chat: true },
      { ...OPTIONS, stt_chat: [] },
    )

    render(<VoiceSettings />)
    expect(
      await screen.findByText(/None of your connected models accepts audio/),
    ).toBeInTheDocument()
  })

  it("says so when no connected provider offers speech to text", async () => {
    stubVoiceAPI({ enabled: false, mode: "cascade" }, { ...OPTIONS, stt: [] })

    render(<VoiceSettings />)
    const note = await screen.findByRole("note")
    expect(note).toHaveTextContent(
      "None of your connected providers offers speech to text. Connect one that does, or turn on “Transcribe with a chat model”.",
    )
    // Transcribing with a chat model takes other models, so the note goes.
    await userEvent.click(
      screen.getByRole("switch", { name: "Transcribe with a chat model" }),
    )
    expect(
      screen.queryByText(/None of your connected providers offers/),
    ).not.toBeInTheDocument()
  })

  it("does not claim missing models before they load", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise<Response>(() => {})),
    )
    render(<VoiceSettings />)
    expect(screen.queryByRole("note")).not.toBeInTheDocument()
  })

  it("previews a voice and tests the microphone before voice is on in Chat", async () => {
    stubVoiceAPI({
      enabled: false,
      mode: "cascade",
      stt_target: "el/scribe_v2",
      tts_target: "el/eleven_v3",
    })

    render(<VoiceSettings />)
    const preview = await screen.findByRole("button", {
      name: "Preview voice",
    })
    const microphone = screen.getByRole("button", { name: "Test microphone" })
    await waitFor(() => expect(preview).toBeEnabled())
    expect(microphone).toBeEnabled()
    expect(
      screen.getByRole("switch", { name: "Enable voice" }),
    ).not.toBeChecked()
  })

  it("waits for a model before previewing or testing", async () => {
    stubVoiceAPI({ enabled: true, mode: "cascade" })

    render(<VoiceSettings />)
    await screen.findByRole("combobox", { name: "Speech-to-text model" })
    expect(screen.getByRole("button", { name: "Preview voice" })).toBeDisabled()
    expect(
      screen.getByRole("button", { name: "Test microphone" }),
    ).toBeDisabled()
  })
})
