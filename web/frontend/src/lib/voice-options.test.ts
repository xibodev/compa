import { describe, expect, it } from "vitest"

import type { VoiceOption } from "@/features/voice/voice-client"

import {
  groupVoiceOptions,
  localeDisplayName,
  voiceLocale,
  voiceOptionProvider,
} from "./voice-options"

const option = (target: string, label = target.split("/")[1]): VoiceOption => ({
  target,
  label,
  provider_kind: "extension",
})

describe("voiceLocale", () => {
  it("finds the language tag a voice names", () => {
    expect(voiceLocale(option("tts/es-MX-DaliaNeural"))).toBe("es-MX")
    expect(voiceLocale(option("tts/af-ZA-AdriNeural"))).toBe("af-ZA")
    expect(voiceLocale(option("tts/es-419-Voice"))).toBe("es-419")
    expect(voiceLocale(option("tts/sr-Latn-RS-NicholasNeural"))).toBe(
      "sr-Latn-RS",
    )
  })

  it("reads the model id when the label is a plain name", () => {
    expect(voiceLocale(option("tts/en-US-AriaNeural", "Aria"))).toBe("en-US")
  })

  it("does not mistake model names for languages", () => {
    for (const name of [
      "gpt-4o-mini-tts",
      "tts-1-hd",
      "eleven_v3",
      "whisper-1",
    ])
      expect(voiceLocale(option(`m/${name}`))).toBeUndefined()
  })
})

describe("groupVoiceOptions", () => {
  const providerOf = () => "Speech Service"

  it("groups voices by language, the UI language's own first", () => {
    const groups = groupVoiceOptions(
      [
        option("tts/af-ZA-AdriNeural"),
        option("tts/es-MX-DaliaNeural"),
        option("tts/en-US-AriaNeural"),
        option("tts/en-GB-RyanNeural"),
        option("el/eleven_v3"),
      ],
      "en",
      providerOf,
    )
    expect(groups.map((group) => group.label)).toEqual([
      "Speech Service",
      "English (United Kingdom)",
      "English (United States)",
      "Afrikaans (South Africa)",
      "Spanish (Mexico)",
    ])
    expect(groups[0].options.map((o) => o.target)).toEqual(["el/eleven_v3"])
  })

  it("names languages in the UI language", () => {
    expect(localeDisplayName("es-MX", "zh")).toBe("西班牙语（墨西哥）")
    expect(localeDisplayName("not a tag", "en")).toBe("not a tag")
  })
})

describe("voiceOptionProvider", () => {
  const labels = {
    byTarget: new Map([["ext-a/v1", "Alpha Voices"]]),
    byInstance: new Map([["ext-b", "Beta Voices"]]),
  }

  it("prefers the option's own instance label, then the known ones", () => {
    expect(
      voiceOptionProvider(
        { ...option("ext-a/v1"), instance_label: "Own Label" },
        labels,
      ),
    ).toBe("Own Label")
    expect(voiceOptionProvider(option("ext-a/v1"), labels)).toBe("Alpha Voices")
    expect(voiceOptionProvider(option("ext-b/v2"), labels)).toBe("Beta Voices")
  })

  it("never shows the generic kind of an extension instance", () => {
    const empty = { byTarget: new Map(), byInstance: new Map() }
    expect(voiceOptionProvider(option("ext-c/v3"), empty)).toBe("ext-c")
    expect(
      voiceOptionProvider(
        { target: "el/v4", label: "v4", provider_kind: "elevenlabs" },
        empty,
      ),
    ).toBe("elevenlabs")
  })
})
