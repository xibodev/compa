import type { VoiceOption } from "@/features/voice/voice-client"

/** A group of voice options, e.g. one language or one provider. */
export interface VoiceOptionGroup {
  /** A stable key: "locale:es-MX" or "provider:<name>". */
  key: string
  label: string
  options: VoiceOption[]
}

// A language-region tag such as es-MX, zh-CN, es-419 or sr-Latn-RS, standing
// on its own inside a voice name like "es-MX-DaliaNeural".
const LOCALE_TAG =
  /(?:^|[^A-Za-z])([a-z]{2,3})(?:-([A-Z][a-z]{3}))?-([A-Z]{2}|\d{3})(?![A-Za-z])/

/** The language tag a voice names in its label or model id, if any. */
export function voiceLocale(option: VoiceOption): string | undefined {
  const slash = option.target.indexOf("/")
  const modelID = slash >= 0 ? option.target.slice(slash + 1) : option.target
  for (const text of [option.label, modelID]) {
    const match = LOCALE_TAG.exec(text)
    if (match) {
      return [match[1], match[2], match[3]].filter(Boolean).join("-")
    }
  }
  return undefined
}

/**
 * The name of a language tag in the UI language, e.g. "Spanish (Mexico)".
 * The standard form keeps every variant of a language together when sorted,
 * where the dialect form would file es-MX under "Mexican Spanish".
 */
export function localeDisplayName(tag: string, uiLanguage: string): string {
  try {
    const names = new Intl.DisplayNames([uiLanguage, "en"], {
      type: "language",
      languageDisplay: "standard",
    })
    return names.of(tag) ?? tag
  } catch {
    return tag
  }
}

/** Where the provider name of an option comes from, best source first. */
export interface VoiceProviderLabels {
  /** Instance labels by exact target. */
  byTarget: ReadonlyMap<string, string>
  /** Instance display names by instance id. */
  byInstance: ReadonlyMap<string, string>
}

/**
 * The name of the provider serving an option. The generic kind of an
 * instance an extension serves says nothing, so it is never shown.
 */
export function voiceOptionProvider(
  option: VoiceOption,
  labels: VoiceProviderLabels,
): string {
  const instanceID = option.target.split("/", 1)[0] ?? ""
  return (
    option.instance_label?.trim() ||
    labels.byTarget.get(option.target) ||
    labels.byInstance.get(instanceID) ||
    (option.provider_kind && option.provider_kind !== "extension"
      ? option.provider_kind
      : instanceID)
  )
}

/**
 * Groups options by the language their voice speaks when their names say,
 * with the UI language's own first, and the rest by provider ahead of them.
 */
export function groupVoiceOptions(
  options: readonly VoiceOption[],
  uiLanguage: string,
  providerOf: (option: VoiceOption) => string,
): VoiceOptionGroup[] {
  const byProvider = new Map<string, VoiceOption[]>()
  const byLocale = new Map<string, VoiceOption[]>()
  for (const option of options) {
    const locale = voiceLocale(option)
    const map = locale ? byLocale : byProvider
    const key = locale ?? providerOf(option)
    const list = map.get(key) ?? []
    list.push(option)
    map.set(key, list)
  }
  const collator = new Intl.Collator(uiLanguage)
  const uiBase = uiLanguage.toLowerCase().split("-")[0]
  const providerGroups = [...byProvider.entries()]
    .map(([name, list]) => ({
      key: `provider:${name}`,
      label: name,
      options: list,
    }))
    .sort((a, b) => collator.compare(a.label, b.label))
  const localeGroups = [...byLocale.entries()]
    .map(([tag, list]) => ({
      key: `locale:${tag}`,
      label: localeDisplayName(tag, uiLanguage),
      options: list,
      own: tag.toLowerCase().split("-")[0] === uiBase,
    }))
    .sort(
      (a, b) =>
        Number(b.own) - Number(a.own) || collator.compare(a.label, b.label),
    )
    .map(({ key, label, options: list }) => ({ key, label, options: list }))
  return [...providerGroups, ...localeGroups]
}
