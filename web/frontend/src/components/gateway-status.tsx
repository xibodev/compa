import {
  IconChevronDown,
  IconPlayerPlay,
  IconPlayerStop,
  IconRefresh,
} from "@tabler/icons-react"
import * as React from "react"
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
import { Button } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useGateway } from "@/hooks/use-gateway"
import { cn } from "@/lib/utils"
import type { GatewayState } from "@/store/gateway"

// Green while it works, grey while it is off, amber while it changes and red
// only when it failed.
const DOT_CLASS: Record<GatewayState, string> = {
  running: "bg-emerald-500",
  stopped: "bg-muted-foreground/60",
  unknown: "bg-muted-foreground/40",
  starting: "animate-pulse bg-amber-500",
  restarting: "animate-pulse bg-amber-500",
  stopping: "animate-pulse bg-amber-500",
  error: "bg-destructive",
}

/**
 * The gateway's state as a labelled indicator in the header. Starting,
 * restarting and stopping live in its menu, and stopping asks first: the
 * gateway starts with Compa, so stopping it is the unusual action.
 */
export function GatewayStatusControl() {
  const { t } = useTranslation()
  const {
    state,
    loading,
    canStart,
    startReason,
    start,
    restart,
    stop,
    error,
  } = useGateway()
  const [confirmStop, setConfirmStop] = React.useState(false)

  const isRunning = state === "running"
  const isBusy =
    loading ||
    state === "starting" ||
    state === "restarting" ||
    state === "stopping"
  const canOfferStart = state === "stopped" || state === "error"
  const statusText = t(`header.gateway.state.${state}`)
  const label = t("header.gateway.statusLabel", { status: statusText })

  return (
    <>
      <DropdownMenu>
        <Tooltip delayDuration={500}>
          <TooltipTrigger asChild>
            <DropdownMenuTrigger asChild>
              <Button
                variant="ghost"
                size="sm"
                aria-label={label}
                className={cn(
                  "border-border/70 h-8 gap-2 rounded-full border px-2.5 sm:px-3",
                  state === "error" && "border-destructive/50 text-destructive",
                )}
              >
                <span
                  aria-hidden="true"
                  className={cn("size-2 shrink-0 rounded-full", DOT_CLASS[state])}
                />
                <span className="text-xs font-medium">{statusText}</span>
                <IconChevronDown className="size-3.5 opacity-60" />
              </Button>
            </DropdownMenuTrigger>
          </TooltipTrigger>
          <TooltipContent>{error ?? label}</TooltipContent>
        </Tooltip>
        <DropdownMenuContent align="end" className="w-72">
          <DropdownMenuLabel className="text-foreground flex items-center gap-2 text-sm">
            <span
              aria-hidden="true"
              className={cn("size-2 shrink-0 rounded-full", DOT_CLASS[state])}
            />
            {label}
          </DropdownMenuLabel>
          <p className="text-muted-foreground px-2 pb-2 text-xs leading-relaxed">
            {t("header.gateway.about")}
          </p>
          {error && (
            <p role="alert" className="text-destructive px-2 pb-2 text-xs">
              {error}
            </p>
          )}
          {!canStart && startReason && canOfferStart && (
            <p className="text-muted-foreground px-2 pb-2 text-xs">
              {startReason}
            </p>
          )}
          {(isRunning || canOfferStart) && <DropdownMenuSeparator />}
          {isRunning && (
            <DropdownMenuItem disabled={isBusy} onSelect={() => void restart()}>
              <IconRefresh />
              {t("header.gateway.action.restart")}
            </DropdownMenuItem>
          )}
          {isRunning && (
            <DropdownMenuItem
              variant="destructive"
              disabled={isBusy}
              onSelect={() => setConfirmStop(true)}
            >
              <IconPlayerStop />
              {t("header.gateway.action.stop")}
            </DropdownMenuItem>
          )}
          {canOfferStart && (
            <DropdownMenuItem
              disabled={isBusy || !canStart}
              onSelect={() => void start()}
            >
              <IconPlayerPlay />
              {t("header.gateway.action.start")}
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>

      <AlertDialog open={confirmStop} onOpenChange={setConfirmStop}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("header.gateway.stopDialog.title")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("header.gateway.stopDialog.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="destructive"
              onClick={() => {
                setConfirmStop(false)
                void stop()
              }}
            >
              {t("header.gateway.stopDialog.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
