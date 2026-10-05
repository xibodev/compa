import { IconHistory, IconTrash } from "@tabler/icons-react"
import dayjs from "dayjs"
import { type RefObject, useState } from "react"
import { useTranslation } from "react-i18next"

import type { SessionSummary } from "@/api/sessions"
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
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ScrollArea } from "@/components/ui/scroll-area"

interface SessionHistoryMenuProps {
  sessions: SessionSummary[]
  activeSessionId: string
  hasMore: boolean
  loadError: boolean
  loadErrorMessage: string
  observerRef: RefObject<HTMLDivElement | null>
  onOpenChange: (open: boolean) => void
  onSwitchSession: (sessionId: string) => void
  onDeleteSession: (sessionId: string) => void
}

export function SessionHistoryMenu({
  sessions,
  activeSessionId,
  hasMore,
  loadError,
  loadErrorMessage,
  observerRef,
  onOpenChange,
  onSwitchSession,
  onDeleteSession,
}: SessionHistoryMenuProps) {
  const { t } = useTranslation()
  const [pendingDelete, setPendingDelete] = useState<SessionSummary | null>(
    null,
  )
  const [historyOpen, setHistoryOpen] = useState(false)

  return (
    <>
      <DropdownMenu
        open={historyOpen}
        onOpenChange={(open) => {
          setHistoryOpen(open)
          onOpenChange(open)
        }}
      >
        <DropdownMenuTrigger asChild>
          <Button
            variant="secondary"
            size="sm"
            className="h-9 gap-2"
            aria-label={t("chat.history")}
          >
            <IconHistory className="size-4" />
            <span className="hidden sm:inline">{t("chat.history")}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-72">
          <ScrollArea className="max-h-[300px]">
            {loadError && (
              <DropdownMenuItem disabled>
                <span className="text-destructive text-xs">
                  {loadErrorMessage}
                </span>
              </DropdownMenuItem>
            )}
            {sessions.length === 0 && !loadError ? (
              <DropdownMenuItem disabled>
                <span className="text-muted-foreground text-xs">
                  {t("chat.noHistory")}
                </span>
              </DropdownMenuItem>
            ) : (
              sessions.map((session) => (
                // Delete is its own menu item rather than a button inside the
                // session's item: the menu moves focus between items only, so
                // a nested button could not be reached from the keyboard.
                <div key={session.id} className="group relative my-0.5">
                  <DropdownMenuItem
                    className={`flex flex-col items-start gap-0.5 pr-8 ${
                      session.id === activeSessionId ? "bg-accent" : ""
                    }`}
                    onClick={() => onSwitchSession(session.id)}
                  >
                    <span className="line-clamp-1 text-sm font-medium">
                      {session.title}
                    </span>
                    <span className="text-muted-foreground text-xs">
                      {t("chat.messagesCount", {
                        count: session.message_count,
                      })}{" "}
                      · {dayjs(session.updated).fromNow()}
                    </span>
                  </DropdownMenuItem>
                  <DropdownMenuItem
                    aria-label={t("chat.deleteSession")}
                    className="text-muted-foreground hover:bg-destructive/10 hover:text-destructive focus:bg-destructive/10 focus:text-destructive absolute top-1/2 right-2 flex h-6 w-6 -translate-y-1/2 items-center justify-center rounded-md p-0 opacity-60 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100"
                    onSelect={() => {
                      setHistoryOpen(false)
                      window.requestAnimationFrame(() =>
                        setPendingDelete(session),
                      )
                    }}
                  >
                    <IconTrash className="h-4 w-4" />
                  </DropdownMenuItem>
                </div>
              ))
            )}
            {hasMore && sessions.length > 0 && (
              <div ref={observerRef} className="py-2 text-center">
                <span className="text-muted-foreground animate-pulse text-xs">
                  {t("chat.loadingMore")}
                </span>
              </div>
            )}
          </ScrollArea>
        </DropdownMenuContent>
      </DropdownMenu>
      <AlertDialog
        open={pendingDelete !== null}
        onOpenChange={(open) => {
          if (!open) setPendingDelete(null)
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("chat.deleteSession")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("chat.deleteSessionConfirm", {
                title: pendingDelete?.title ?? "",
              })}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                if (pendingDelete) onDeleteSession(pendingDelete.id)
                setPendingDelete(null)
              }}
            >
              {t("chat.deleteSession")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
