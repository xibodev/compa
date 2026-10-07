import { IconArrowLeft, IconCompass } from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"

/** Any address the app has no page for. */
export function NotFoundPage() {
  const { t } = useTranslation()
  return (
    <div className="flex h-full flex-col items-center justify-center px-6 py-16 text-center">
      <div className="bg-muted text-muted-foreground mb-6 flex size-16 items-center justify-center rounded-2xl">
        <IconCompass className="size-8" />
      </div>
      <p className="text-muted-foreground mb-1 text-sm font-medium tracking-wider">
        404
      </p>
      <h1 className="text-foreground mb-2 text-2xl font-semibold tracking-tight">
        {t("notFound.title")}
      </h1>
      <p className="text-muted-foreground mb-6 max-w-sm text-sm">
        {t("notFound.description")}
      </p>
      <Button asChild>
        <Link to="/">
          <IconArrowLeft className="size-4" />
          {t("notFound.backToChat")}
        </Link>
      </Button>
    </div>
  )
}
