import { useBlocker } from "@tanstack/react-router"
import { useTranslation } from "react-i18next"

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"

/**
 * Asks before leaving a page with unsaved edits, whether by an in-app link
 * or by closing or reloading the tab.
 */
export function UnsavedChangesGuard({ when }: { when: boolean }) {
  const { t } = useTranslation()
  const { status, proceed, reset } = useBlocker({
    shouldBlockFn: () => when,
    enableBeforeUnload: () => when,
    withResolver: true,
  })

  return (
    <AlertDialog
      open={status === "blocked"}
      onOpenChange={(open) => {
        if (!open) reset?.()
      }}
    >
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>{t("common.saveChangesTitle")}</AlertDialogTitle>
          <AlertDialogDescription>
            {t("pages.config.unsaved_changes")}
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
          <AlertDialogAction variant="destructive" onClick={() => proceed?.()}>
            {t("common.discard")}
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}
