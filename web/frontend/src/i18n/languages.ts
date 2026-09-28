/**
 * The interface languages, each named in its own language so a reader who
 * does not understand the current one can still find theirs.
 */
export const UI_LANGUAGES = [
  { code: "en", label: "English" },
  { code: "pt-BR", label: "Português (Brasil)" },
  { code: "bn-IN", label: "বাংলা" },
  { code: "zh", label: "简体中文" },
  { code: "cs", label: "Čeština" },
] as const

/**
 * The UI language that best matches an i18next language tag, e.g. "en-US"
 * is "en" and "pt-br" is "pt-BR".
 */
export function matchUILanguage(tag: string | undefined): string {
  const lower = (tag ?? "").toLowerCase()
  const exact = UI_LANGUAGES.find((lang) => lang.code.toLowerCase() === lower)
  if (exact) return exact.code
  const base = lower.split("-")[0]
  return (
    UI_LANGUAGES.find((lang) => lang.code.toLowerCase().split("-")[0] === base)
      ?.code ?? "en"
  )
}
