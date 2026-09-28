import dayjs from "dayjs"
import "dayjs/locale/bn"
import "dayjs/locale/cs"
import "dayjs/locale/en"
import "dayjs/locale/pt-br"
import "dayjs/locale/zh-cn"
import localizedFormat from "dayjs/plugin/localizedFormat"
import relativeTime from "dayjs/plugin/relativeTime"
import i18n from "i18next"
import LanguageDetector from "i18next-browser-languagedetector"
import { initReactI18next } from "react-i18next"

import { matchUILanguage } from "./languages"
import bnIn from "./locales/bn-in.json"
import cs from "./locales/cs.json"
import en from "./locales/en.json"
import ptBr from "./locales/pt-br.json"
import zh from "./locales/zh.json"

dayjs.extend(relativeTime)
dayjs.extend(localizedFormat)

/**
 * Follows the UI language everywhere the browser formats on its own: dates
 * through dayjs, and numbers, spelling and hyphenation through <html lang>.
 */
function applyLanguage(lng: string) {
  if (lng.startsWith("zh")) {
    dayjs.locale("zh-cn")
  } else if (lng.startsWith("pt")) {
    dayjs.locale("pt-br")
  } else if (lng.startsWith("bn")) {
    dayjs.locale("bn")
  } else if (lng.startsWith("cs")) {
    dayjs.locale("cs")
  } else {
    dayjs.locale("en")
  }
  if (typeof document !== "undefined") {
    document.documentElement.lang = matchUILanguage(lng)
  }
}

// Registered before init so the initial language is applied too.
i18n.on("languageChanged", applyLanguage)

i18n
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
      "pt-BR": {
        translation: ptBr,
      },
      "bn-IN": {
        translation: bnIn,
      },
      zh: {
        translation: zh,
      },
      cs: {
        translation: cs,
      },
    },
    fallbackLng: "en",
    debug: false,

    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
  })

if (i18n.language) applyLanguage(i18n.language)

export default i18n
