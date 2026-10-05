import { useState } from "react"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/page-header"
import { Button } from "@/components/ui/button"
import { useDefaultModel } from "@/hooks/use-default-model"
import { useProviderInstances } from "@/hooks/use-provider-instances"
import { cn } from "@/lib/utils"

import { DefaultModelBar } from "./default-model"
import { ProvidersPanel } from "./providers-panel"
import { RoutesPanel } from "./routes-panel"

type Section = "providers" | "routes"

export function ModelsWorkspace() {
  const { t } = useTranslation()
  const [section, setSection] = useState<Section>("providers")
  const state = useProviderInstances()
  const defaultModel = useDefaultModel()
  // Changing or removing an instance can clear the default model on the
  // server, and the first model added to Chat can become it, so every
  // management refresh reloads it as well.
  const refresh = async () => {
    await Promise.all([state.refresh(), defaultModel.refresh()])
  }
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.models")} />
      <div
        className="border-border/60 flex gap-1 border-b px-4 sm:px-6"
        role="tablist"
      >
        {(["providers", "routes"] as const).map((item) => (
          <Button
            key={item}
            role="tab"
            id={`models-tab-${item}`}
            aria-selected={section === item}
            aria-controls="models-tabpanel"
            variant="ghost"
            className={cn(
              "rounded-none border-x-0 border-t-0",
              section === item
                ? "bg-muted text-foreground shadow-[inset_0_-1px_0_hsl(var(--foreground))]"
                : "text-muted-foreground border-transparent",
            )}
            onClick={() => setSection(item)}
          >
            {t(`models.management.tabs.${item}`)}
          </Button>
        ))}
      </div>
      <div
        id="models-tabpanel"
        className="min-h-0 flex-1 overflow-y-auto px-4 py-6 sm:px-6"
        role="tabpanel"
        aria-labelledby={`models-tab-${section}`}
      >
        <div className="mx-auto max-w-6xl">
          <DefaultModelBar
            defaultModel={defaultModel}
            targets={state.targets}
          />
          {state.error && (
            <p role="alert" className="text-destructive mb-4 text-sm">
              {state.error}
            </p>
          )}
          {state.loading &&
          state.instances.length === 0 &&
          state.roster.length === 0 ? (
            <p className="text-muted-foreground text-sm">
              {t("labels.loading")}
            </p>
          ) : section === "providers" ? (
            <ProvidersPanel
              instances={state.instances}
              catalogs={state.catalogs}
              targets={state.targets}
              roster={state.roster}
              refresh={refresh}
              defaultModel={defaultModel}
            />
          ) : (
            <RoutesPanel
              targets={state.targets}
              routes={state.routes}
              refresh={refresh}
              defaultModel={defaultModel}
            />
          )}
        </div>
      </div>
    </div>
  )
}
