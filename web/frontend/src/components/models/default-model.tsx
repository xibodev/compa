import { IconStar } from "@tabler/icons-react"
import { useTranslation } from "react-i18next"

import type { ProviderTarget } from "@/api/provider-instances"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import type { DefaultModelState } from "@/hooks/use-default-model"
import { selectionLabel } from "@/lib/model-labels"

/** The configured default model, shown above both management tabs. */
export function DefaultModelBar({
  defaultModel,
  targets,
}: {
  defaultModel: DefaultModelState
  targets: readonly ProviderTarget[]
}) {
  const { t } = useTranslation()
  const { selection, loaded, saving, error, setDefault } = defaultModel
  const loadFailed = !loaded && error !== ""
  const label = selection ? selectionLabel(selection, targets) : null
  return (
    <section
      aria-label={t("models.management.default.label")}
      className="border-border bg-card mb-6 flex flex-col gap-3 rounded-xl border px-4 py-3 shadow-xs sm:flex-row sm:items-center sm:justify-between"
    >
      <div className="min-w-0">
        <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-sm">
          <IconStar className="size-4 shrink-0 text-amber-500" />
          <span className="text-muted-foreground shrink-0">
            {t("models.management.default.label")}
          </span>
          {label ? (
            <span
              className="flex min-w-0 items-baseline gap-1.5"
              title={selection}
            >
              <strong className="text-foreground truncate font-semibold">
                {label.model}
              </strong>
              {label.provider && (
                <span className="text-muted-foreground truncate text-xs">
                  {label.provider}
                </span>
              )}
            </span>
          ) : (
            <span
              className={loadFailed ? "text-destructive" : "text-foreground"}
            >
              {loaded
                ? t("models.management.default.none")
                : loadFailed
                  ? t("models.management.default.loadFailed", { error })
                  : t("labels.loading")}
            </span>
          )}
        </div>
        {loaded && !selection && (
          <p className="text-muted-foreground mt-1 text-xs">
            {t("models.management.default.noneHint")}
          </p>
        )}
      </div>
      {selection && (
        <Button
          size="sm"
          variant="ghost"
          className="shrink-0 self-start sm:self-auto"
          disabled={saving}
          onClick={() => void setDefault("")}
        >
          {t("models.management.default.clear")}
        </Button>
      )}
    </section>
  )
}

/**
 * The default marker of a selectable target or route: a badge on the current
 * default, otherwise a button that makes it the default.
 */
export function DefaultModelAction({
  selection,
  name,
  defaultModel,
}: {
  selection: string
  /** How the selection reads to a person; the selection itself if unset. */
  name?: string
  defaultModel: DefaultModelState
}) {
  const { t } = useTranslation()
  if (defaultModel.selection === selection)
    return (
      <Badge variant="secondary" className="gap-1">
        <IconStar className="size-3 text-amber-500" />
        {t("models.management.default.badge")}
      </Badge>
    )
  return (
    <Button
      size="sm"
      variant="ghost"
      className="h-7 px-2 text-xs"
      disabled={defaultModel.saving}
      aria-label={t("models.management.default.setFor", {
        selection: name || selection,
      })}
      title={selection}
      onClick={() => void defaultModel.setDefault(selection)}
    >
      <IconStar className="mr-1 size-3.5" />
      <span className="hidden sm:inline">
        {t("models.management.default.set")}
      </span>
    </Button>
  )
}
