import { IconPlus } from "@tabler/icons-react"
import {
  ErrorComponent,
  type ErrorComponentProps,
} from "@tanstack/react-router"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { newChatSession } from "@/features/chat/controller"

/**
 * What the chat page shows when it fails to render. Its own New Chat button
 * is part of the page that failed, and the failing conversation is restored
 * on every visit, so the way out is offered here.
 */
export function ChatRouteError({ error, reset }: ErrorComponentProps) {
  const { t } = useTranslation()

  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 p-6">
      <ErrorComponent error={error} />
      <Button
        variant="secondary"
        size="sm"
        className="h-9 gap-2"
        onClick={() => void newChatSession().finally(reset)}
      >
        <IconPlus className="size-4" />
        {t("chat.newChat")}
      </Button>
    </div>
  )
}
