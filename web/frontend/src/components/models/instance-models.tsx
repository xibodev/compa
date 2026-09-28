import { IconCheck, IconPlus, IconSearch } from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"
import { useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ProviderCatalogModel,
  type ProviderInstance,
  type ProviderTarget,
  servesChat,
} from "@/api/provider-instances"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"

import { speaksOnly, surfacesText } from "./provider-model"

// A catalog longer than this gets a search box above its list.
const SEARCH_FROM = 30
// How many rows the list shows at first, and how many more each "Show more" adds.
const PAGE_SIZE = 50

interface InstanceModelsProps {
  instance: ProviderInstance
  models: readonly ProviderCatalogModel[]
  targets: readonly ProviderTarget[]
  activeModels: readonly string[]
  onToggleShortlist: (target: string) => Promise<void>
}

/**
 * The models of one connection. A text-to-speech provider's voices are only
 * counted, with a way to the Voice page where one is chosen; a long catalog
 * is searched rather than scrolled, and never rendered all at once.
 */
export function InstanceModels({
  instance,
  models,
  targets,
  activeModels,
  onToggleShortlist,
}: InstanceModelsProps) {
  const { t } = useTranslation()
  const [query, setQuery] = useState("")
  const [limit, setLimit] = useState(PAGE_SIZE)

  if (models.length === 0) return null

  if (speaksOnly(models)) {
    return (
      <div className="border-border bg-muted/20 mt-4 flex flex-wrap items-center justify-between gap-x-4 gap-y-1 rounded-lg border px-3 py-2.5 text-xs">
        <span className="text-foreground font-medium">
          {t("models.inspector.voices", { count: models.length })}
        </span>
        <Link
          to="/config/voice"
          className="text-primary font-medium underline-offset-4 hover:underline"
        >
          {t("models.inspector.voicesLink")}
        </Link>
      </div>
    )
  }

  const labels = new Map(targets.map((item) => [item.target, item.label]))
  const rows = models.map((model) => {
    const target = `${instance.id}/${model.id}`
    const name =
      labels.get(target)?.trim() || model.display_name?.trim() || model.id
    return { model, target, name }
  })
  const searchable = models.length > SEARCH_FROM
  const needle = searchable ? query.trim().toLowerCase() : ""
  const matches = needle
    ? rows.filter(
        (row) =>
          row.name.toLowerCase().includes(needle) ||
          row.model.id.toLowerCase().includes(needle),
      )
    : rows
  const shown = matches.slice(0, limit)

  return (
    <div className="mt-4 pt-2">
      <h5 className="text-foreground mb-2 text-xs font-semibold">
        {t("models.inspector.modelList", { models: models.length })}
      </h5>
      {searchable && (
        <div className="relative mb-2">
          <IconSearch className="text-muted-foreground absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2" />
          <Input
            type="search"
            value={query}
            onChange={(event) => {
              setQuery(event.target.value)
              setLimit(PAGE_SIZE)
            }}
            placeholder={t("models.inspector.searchModels")}
            aria-label={t("models.inspector.searchModels")}
            className="h-8 pl-8 text-xs"
          />
        </div>
      )}
      {shown.length === 0 ? (
        <p className="text-muted-foreground rounded-lg border border-dashed p-3 text-center text-xs">
          {t("models.inspector.noModelMatch")}
        </p>
      ) : (
        <ul className="border-border divide-border/60 bg-muted/20 max-h-60 divide-y overflow-y-auto rounded-lg border">
          {shown.map(({ model, target, name }) => {
            const inChat = activeModels.includes(target)
            const chat = servesChat(model.surfaces)
            return (
              <li
                key={model.id}
                className="flex items-center justify-between gap-3 p-2 px-3 text-xs"
                title={target}
              >
                <span className="min-w-0">
                  <span className="text-foreground block truncate font-medium">
                    {name}
                  </span>
                  {name !== model.id && (
                    <span className="text-muted-foreground block truncate font-mono text-[11px]">
                      {model.id}
                    </span>
                  )}
                </span>
                {/* A model that serves no chat surface, such as a
                    speech-only one, cannot join Chat. */}
                {!chat && !inChat ? (
                  <span className="text-muted-foreground shrink-0 text-[11px]">
                    {surfacesText(model.surfaces ?? [], t)}
                  </span>
                ) : (
                  <Button
                    size="sm"
                    variant={inChat ? "outline" : "secondary"}
                    className="h-6 shrink-0 px-2 text-[11px]"
                    onClick={() => void onToggleShortlist(target)}
                  >
                    {inChat ? (
                      <>
                        <IconCheck className="mr-1 size-3 text-emerald-500" />
                        {t("models.inChat.inChat")}
                      </>
                    ) : (
                      <>
                        <IconPlus className="mr-1 size-3" />
                        {t("models.inChat.add")}
                      </>
                    )}
                  </Button>
                )}
              </li>
            )
          })}
        </ul>
      )}
      {matches.length > shown.length && (
        <div className="mt-2 flex flex-wrap items-center justify-between gap-2">
          <span className="text-muted-foreground text-[11px]">
            {t("models.inspector.showing", {
              shown: shown.length,
              total: matches.length,
            })}
          </span>
          <Button
            size="sm"
            variant="outline"
            className="h-7 text-xs"
            onClick={() => setLimit((current) => current + PAGE_SIZE)}
          >
            {t("models.inspector.showMore")}
          </Button>
        </div>
      )}
    </div>
  )
}
