import dayjs from "dayjs"
import "dayjs/locale/bn"
import "dayjs/locale/cs"
import "dayjs/locale/en"
import "dayjs/locale/pt-br"
import "dayjs/locale/zh-cn"
import localizedFormat from "dayjs/plugin/localizedFormat"
import relativeTime from "dayjs/plugin/relativeTime"
import i18n, { type BackendModule, type ResourceKey } from "i18next"
import LanguageDetector from "i18next-browser-languagedetector"
import { initReactI18next } from "react-i18next"

import { UI_LANGUAGES, matchUILanguage } from "./languages"
import en from "./locales/en.json"

dayjs.extend(relativeTime)
dayjs.extend(localizedFormat)

const DAYJS_LOCALES: Record<string, string> = {
  en: "en",
  "pt-BR": "pt-br",
  "bn-IN": "bn",
  zh: "zh-cn",
  cs: "cs",
}

/**
 * Follows the UI language everywhere the browser formats on its own: dates
 * through dayjs, and numbers, spelling and hyphenation through <html lang>.
 * It uses the language whose text is actually shown, so a translation that
 * failed to load does not leave English text with foreign dates.
 */
function applyLanguage(lng: string) {
  const language = matchUILanguage(i18n.resolvedLanguage ?? lng)
  dayjs.locale(DAYJS_LOCALES[language] ?? "en")
  if (typeof document !== "undefined") {
    document.documentElement.lang = language
  }
}

// English is bundled as the fallback; every other language is fetched only
// when it is the one in use.
const LOCALE_LOADERS: Record<string, () => Promise<{ default: ResourceKey }>> =
  {
    "pt-BR": () => import("./locales/pt-br.json"),
    "bn-IN": () => import("./locales/bn-in.json"),
    zh: () => import("./locales/zh.json"),
    cs: () => import("./locales/cs.json"),
  }

const lazyLocales: BackendModule = {
  type: "backend",
  init() {},
  read(language, _namespace, callback) {
    const load = LOCALE_LOADERS[language]
    if (!load) {
      callback(null, {})
      return
    }
    load().then(
      (locale) => callback(null, locale.default),
      (error: unknown) =>
        callback(error instanceof Error ? error : String(error), null),
    )
  },
}

// Registered before init so the initial language is applied too.
i18n.on("languageChanged", applyLanguage)

i18n
  .use(lazyLocales)
  // detect user language
  // learn more: https://github.com/i18next/i18next-browser-languageDetector
  .use(LanguageDetector)
  // pass the i18n instance to react-i18next.
  .use(initReactI18next)
  // init i18next
  // for all options read: https://www.i18next.com/overview/configuration-options
  .init({
    resources: {
      en: {
        translation: en,
      },
    },
    partialBundledLanguages: true,
    // Browsers report bare or other regional tags (pt, pt-PT, bn, bn-BD,
    // zh-CN); only the listed languages exist, and each tag resolves to the
    // one that shares its language.
    supportedLngs: UI_LANGUAGES.map((language) => language.code),
    fallbackLng: {
      pt: ["pt-BR", "en"],
      bn: ["bn-IN", "en"],
      default: ["en"],
    },
    debug: false,

    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
  })

if (i18n.language) applyLanguage(i18n.language)

export default i18n
