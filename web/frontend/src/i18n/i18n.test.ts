import dayjs from "dayjs"
import { afterAll, describe, expect, it } from "vitest"

import i18n from "@/i18n"

import bnIn from "./locales/bn-in.json"
import cs from "./locales/cs.json"
import en from "./locales/en.json"
import ptBr from "./locales/pt-br.json"
import zh from "./locales/zh.json"

type Tree = { [key: string]: string | Tree }

function flatten(tree: Tree, prefix = ""): Record<string, string> {
  const out: Record<string, string> = {}
  for (const [key, value] of Object.entries(tree)) {
    const path = prefix ? `${prefix}.${key}` : key
    if (typeof value === "string") out[path] = value
    else Object.assign(out, flatten(value, path))
  }
  return out
}

const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/

function nonPluralKeys(tree: Tree) {
  return Object.keys(flatten(tree)).filter((key) => !PLURAL_SUFFIX.test(key))
}

describe("UI language", () => {
  afterAll(async () => {
    await i18n.changeLanguage("en")
  })

  it.each([
    ["pt", "pt-BR", "pt-br"],
    ["pt-PT", "pt-BR", "pt-br"],
    ["bn", "bn-IN", "bn"],
    ["bn-BD", "bn-IN", "bn"],
    ["zh-CN", "zh", "zh-cn"],
    ["cs-CZ", "cs", "cs"],
    ["en-US", "en", "en"],
  ])(
    "shows %s in the translation that shares its language",
    async (tag, ui, dayjsLocale) => {
      await i18n.changeLanguage(tag)

      expect(i18n.resolvedLanguage).toBe(ui)
      expect(document.documentElement.lang).toBe(ui)
      expect(dayjs.locale()).toBe(dayjsLocale)
    },
  )

  it("loads the chosen translation's text", async () => {
    await i18n.changeLanguage("pt-PT")
    expect(i18n.t("common.cancel")).toBe("Cancelar")
  })
})

describe("locale files", () => {
  const locales: Record<string, Tree> = {
    cs,
    "pt-br": ptBr,
    zh,
    "bn-in": bnIn,
  }

  it.each(Object.keys(locales))("%s has every English key", (name) => {
    const keys = new Set(nonPluralKeys(locales[name]))
    const missing = nonPluralKeys(en).filter((key) => !keys.has(key))
    expect(missing).toEqual([])
  })

  it("gives Czech every plural form a whole count needs", () => {
    const czech = flatten(cs)
    const bases = Object.keys(flatten(en))
      .filter((key) => key.endsWith("_one"))
      .map((key) => key.slice(0, -"_one".length))
    const incomplete = bases.filter(
      (base) =>
        !(`${base}_one` in czech) ||
        !(`${base}_few` in czech) ||
        !(`${base}_other` in czech),
    )
    expect(incomplete).toEqual([])
  })
})
