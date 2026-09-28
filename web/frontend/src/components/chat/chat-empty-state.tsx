import {
  IconPlugConnectedX,
  IconRobot,
  IconRobotOff,
  IconStar,
} from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import type { ChatModelAvailability } from "@/features/chat/model-availability"
import type { GatewayState } from "@/store/gateway"

// The gateway states in which it is known not to run. While its state is
// still being checked, or while it starts, the chat welcomes as usual.
const GATEWAY_DOWN: ReadonlySet<GatewayState> = new Set([
  "stopped",
  "stopping",
  "error",
])

interface ChatEmptyStateProps {
  modelAvailability: ChatModelAvailability
  gatewayState: GatewayState
  /** Installed modules can be picked in the Module menu beside the model. */
  hasModules?: boolean
}

export function ChatEmptyState({
  modelAvailability,
  gatewayState,
  hasModules = false,
}: ChatEmptyStateProps) {
  const { t } = useTranslation()

  if (modelAvailability === "unavailable") {
    return (
      <div className="flex flex-col items-center justify-center py-20">
        <div className="mb-6 flex h-16 w-16 items-center justify-center rounded-2xl bg-amber-500/10 text-amber-500">
          <IconRobotOff className="h-8 w-8" />
        </div>
        <h3 className="mb-2 text-xl font-medium">
          {t("chat.empty.noModelTitle")}
        </h3>
        <p className="text-muted-foreground mb-4 max-w-md text-center text-sm">
          {t("chat.empty.noModelDescription")}
        </p>
        <Button asChild size="sm" className="px-4">
          <Link to="/models">{t("chat.empty.setUpModels")}</Link>
        </Button>
      </div>
    )
  }

  if (modelAvailability === "unselected") {
    return (
      <div className="flex flex-col items-center justify-center py-20">
        <div className="mb-6 flex h-16 w-16 items-center justify-center rounded-2xl bg-amber-500/10 text-amber-500">
          <IconStar className="h-8 w-8" />
        </div>
        <h3 className="mb-2 text-xl font-medium">
          {t("chat.empty.noSelectedModel")}
        </h3>
        <p className="text-muted-foreground mb-4 max-w-md text-center text-sm">
          {t("chat.empty.noSelectedModelDescription")}
        </p>
        <Button asChild variant="outline" size="sm" className="px-4">
          <Link to="/models">{t("chat.empty.goToModels")}</Link>
        </Button>
      </div>
    )
  }

  if (GATEWAY_DOWN.has(gatewayState)) {
    return (
      <div className="flex flex-col items-center justify-center py-20">
        <div className="mb-6 flex h-16 w-16 items-center justify-center rounded-2xl bg-amber-500/10 text-amber-500">
          <IconPlugConnectedX className="h-8 w-8" />
        </div>
        <h3 className="mb-2 text-xl font-medium">
          {t("chat.empty.notRunning")}
        </h3>
        <p className="text-muted-foreground mb-4 max-w-md text-center text-sm">
          {t("chat.empty.notRunningDescription")}
        </p>
      </div>
    )
  }

  return (
    <div className="flex flex-col items-center justify-center py-20">
      <div className="mb-6 flex h-16 w-16 items-center justify-center rounded-2xl bg-violet-500/10 text-violet-500">
        <IconRobot className="h-8 w-8" />
      </div>
      <h3 className="mb-2 text-xl font-medium">{t("chat.welcome")}</h3>
      <p className="text-muted-foreground max-w-md text-center text-sm">
        {t("chat.welcomeDesc")}
      </p>
      {hasModules && (
        <p className="text-muted-foreground mt-2 max-w-md text-center text-sm">
          {t("chat.welcomeModules")}
        </p>
      )}
    </div>
  )
}
