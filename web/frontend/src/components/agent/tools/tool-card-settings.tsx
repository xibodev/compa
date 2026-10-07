import { useQuery, useQueryClient } from "@tanstack/react-query"
import { type ReactNode, useId, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type AppConfig, getAppConfig, patchAppConfig } from "@/api/channels"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

type MessageTargets = "current_chat" | "any"

function toolConfig(config: AppConfig | undefined, tool: string) {
  const tools = config?.tools
  if (typeof tools !== "object" || tools === null) return {}
  const value = (tools as Record<string, unknown>)[tool]
  return typeof value === "object" && value !== null
    ? (value as Record<string, unknown>)
    : {}
}

/**
 * One tool setting read from the config and saved as soon as it changes,
 * as the tool switches are.
 */
function useToolSetting<T>(
  read: (config: AppConfig | undefined) => T,
  patch: (value: T) => Record<string, unknown>,
) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const { data } = useQuery({ queryKey: ["config"], queryFn: getAppConfig })
  // The chosen value while it is saved; the saved config afterwards.
  const [pending, setPending] = useState<{ value: T } | null>(null)

  const save = async (value: T) => {
    setPending({ value })
    try {
      await patchAppConfig({ tools: patch(value) })
      await queryClient.invalidateQueries({ queryKey: ["config"] })
      const gateway = await refreshGatewayState({ force: true })
      showSaveSuccessOrRestartToast(
        t,
        t("pages.config.save_success"),
        t("navigation.tools", "Tools"),
        gateway?.restartRequired === true,
      )
    } catch (error) {
      toast.error(
        error instanceof Error ? error.message : t("pages.config.save_error"),
      )
    } finally {
      setPending(null)
    }
  }

  return {
    value: pending ? pending.value : read(data),
    loaded: data !== undefined,
    saving: pending !== null,
    save,
  }
}

function SettingRow({
  id,
  label,
  children,
}: {
  id: string
  label: string
  children: ReactNode
}) {
  return (
    <div className="border-border/40 mt-4 flex items-center justify-between gap-3 border-t pt-4">
      <label htmlFor={id} className="text-muted-foreground text-[13px]">
        {label}
      </label>
      {children}
    </div>
  )
}

function MessageTargetsSetting() {
  const { t } = useTranslation()
  const id = useId()
  const { value, loaded, saving, save } = useToolSetting<MessageTargets>(
    (config) =>
      toolConfig(config, "message").targets === "any" ? "any" : "current_chat",
    (targets) => ({ message: { targets } }),
  )
  const label = t("pages.agent.tools.message_targets")

  return (
    <SettingRow id={id} label={label}>
      <Select
        value={value}
        disabled={!loaded || saving}
        onValueChange={(next) => void save(next as MessageTargets)}
      >
        <SelectTrigger id={id} size="sm" className="w-36" aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent align="end">
          <SelectItem value="current_chat">
            {t("pages.agent.tools.message_targets_current_chat")}
          </SelectItem>
          <SelectItem value="any">
            {t("pages.agent.tools.message_targets_any")}
          </SelectItem>
        </SelectContent>
      </Select>
    </SettingRow>
  )
}

/** The settings a tool has beside its switch, on its card. */
export function ToolCardSettings({ toolName }: { toolName: string }) {
  if (toolName === "message") return <MessageTargetsSetting />
  return null
}
