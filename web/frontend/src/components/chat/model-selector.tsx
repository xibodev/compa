import { useMemo } from "react"
import { useTranslation } from "react-i18next"

import type { ModelRoute, ProviderTarget } from "@/api/provider-instances"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectSeparator,
  SelectTrigger,
} from "@/components/ui/select"
import {
  formatModelLabel,
  selectionLabel,
  targetInstanceLabel,
  targetModelLabel,
} from "@/lib/model-labels"

import { chatSelectionValues } from "./model-selector.utils"

const CONFIGURED_DEFAULT = "__configured_default__"

interface ModelSelectorProps {
  selection: string
  /**
   * The configured default model selection, empty when none is set and null
   * while it is not known.
   */
  defaultSelection: string | null
  targets: ProviderTarget[]
  routes: ModelRoute[]
  disabled?: boolean
  onValueChange: (selection: string) => void
}

/**
 * The chat's model. Models read by their names and are grouped by the
 * provider serving them; the exact target stays in each tooltip.
 */
export function ModelSelector({
  selection,
  defaultSelection,
  targets,
  routes,
  disabled = false,
  onValueChange,
}: ModelSelectorProps) {
  const { t } = useTranslation()
  const values = chatSelectionValues(targets, routes)

  const defaultLabel =
    defaultSelection === null
      ? t("chat.selection.configuredDefault")
      : defaultSelection
        ? t("chat.selection.defaultModel", {
            model: formatModelLabel(selectionLabel(defaultSelection, targets)),
          })
        : t("chat.selection.noDefault")

  const groups = useMemo(() => {
    const map = new Map<string, ProviderTarget[]>()
    for (const target of targets) {
      const key = targetInstanceLabel(target)
      const list = map.get(key) || []
      list.push(target)
      map.set(key, list)
    }
    return Array.from(map.entries())
  }, [targets])

  const triggerText = selection
    ? formatModelLabel(selectionLabel(selection, targets))
    : defaultLabel
  const triggerTitle = selection || defaultSelection || undefined

  return (
    <Select
      value={selection || CONFIGURED_DEFAULT}
      onValueChange={(value) =>
        onValueChange(value === CONFIGURED_DEFAULT ? "" : value)
      }
      disabled={disabled}
    >
      <SelectTrigger
        size="sm"
        className="h-8 max-w-[46vw] min-w-0 bg-transparent font-medium shadow-none sm:max-w-[300px] sm:min-w-[150px]"
        aria-label={t("chat.selection.modelLabel")}
        title={triggerTitle}
      >
        <span className="truncate">{triggerText}</span>
      </SelectTrigger>
      <SelectContent position="popper" align="start" className="max-h-80">
        <SelectItem value={CONFIGURED_DEFAULT} title={defaultSelection || undefined}>
          {defaultLabel}
        </SelectItem>
        {routes.length > 0 && <SelectSeparator />}
        {routes.length > 0 && (
          <SelectGroup>
            <SelectLabel>{t("chat.selection.routes")}</SelectLabel>
            {values.routes.map((route) => (
              <SelectItem key={route} value={route}>
                {route}
              </SelectItem>
            ))}
          </SelectGroup>
        )}
        {groups.map(([provider, providerTargets]) => (
          <div key={provider}>
            <SelectSeparator />
            <SelectGroup>
              <SelectLabel className="text-muted-foreground text-xs font-semibold">
                {provider}{" "}
                <span className="font-normal tabular-nums">
                  ({providerTargets.length})
                </span>
              </SelectLabel>
              {providerTargets.map((item) => (
                <SelectItem
                  key={item.target}
                  value={item.target}
                  title={item.target}
                >
                  {targetModelLabel(item)}
                </SelectItem>
              ))}
            </SelectGroup>
          </div>
        ))}
      </SelectContent>
    </Select>
  )
}
