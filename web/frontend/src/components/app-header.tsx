import { IconLogout, IconMenu2, IconRefresh } from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"
import * as React from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  postLauncherDashboardLogout,
  postLauncherDashboardLogoutAll,
} from "@/api/launcher-auth"
import { BrandLogo } from "@/components/brand-logo"
import { GatewayStatusControl } from "@/components/gateway-status"
import { LanguageMenu, ThemeToggle } from "@/components/header-controls"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog.tsx"
import { Button } from "@/components/ui/button.tsx"
import { Separator } from "@/components/ui/separator.tsx"
import { SidebarTrigger } from "@/components/ui/sidebar"
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip"
import { useGateway } from "@/hooks/use-gateway.ts"
import { navigateTo } from "@/lib/navigate"

export function AppHeader() {
  const { t } = useTranslation()
  const {
    state: gwState,
    loading: gwLoading,
    canStart,
    restartRequired,
    restart,
  } = useGateway()

  const isRestarting = gwState === "restarting"
  const isStopping = gwState === "stopping"
  const showNotConnectedHint =
    !isRestarting &&
    !isStopping &&
    canStart &&
    (gwState === "stopped" || gwState === "error")

  const [showLogoutDialog, setShowLogoutDialog] = React.useState(false)

  const handleLogout = async () => {
    await postLauncherDashboardLogout()
    navigateTo("/launcher-login")
  }

  const handleLogoutAll = async () => {
    const result = await postLauncherDashboardLogoutAll().catch(() => null)
    if (!result?.ok) {
      toast.error(result?.error ?? t("launcherLogin.errorNetwork"))
      return
    }
    navigateTo("/launcher-login")
  }

  const handleGatewayRestart = () => {
    if (gwLoading || isRestarting || !restartRequired || !canStart) return
    void restart()
  }

  return (
    <header className="bg-background/95 supports-backdrop-filter:bg-background/60 border-b-border/50 sticky top-0 z-50 flex h-14 shrink-0 items-center justify-between border-b px-3 backdrop-blur sm:px-4">
      <div className="flex min-w-0 items-center gap-2">
        <SidebarTrigger className="text-muted-foreground hover:bg-accent hover:text-foreground flex h-9 w-9 items-center justify-center rounded-lg sm:hidden [&>svg]:size-5">
          <IconMenu2 />
        </SidebarTrigger>
        <Link
          to="/"
          className="focus-visible:ring-ring/50 flex shrink-0 items-center rounded-md outline-none focus-visible:ring-3"
          aria-label={t("header.home")}
        >
          <BrandLogo
            decorative
            nameClassName="hidden sm:inline"
            markClassName="size-7"
          />
        </Link>
      </div>

      {/* Center prominent connection status */}
      <div className="pointer-events-none absolute left-1/2 hidden h-full -translate-x-1/2 items-center justify-center lg:flex">
        {showNotConnectedHint && (
          <div className="text-muted-foreground flex items-center gap-2 rounded-full border border-dashed px-4 py-1.5 text-xs shadow-sm backdrop-blur-md">
            {/* Red only when the gateway failed; a stopped gateway is a
                state to notice, not an error. */}
            {gwState === "error" ? (
              <span className="bg-destructive/50 relative flex size-2 shrink-0 items-center justify-center rounded-full">
                <span className="bg-destructive absolute inline-flex size-full animate-ping rounded-full opacity-75"></span>
              </span>
            ) : (
              <span className="size-2 shrink-0 rounded-full bg-amber-500" />
            )}
            {t("chat.notConnected")}
          </div>
        )}
      </div>

      <AlertDialog open={showLogoutDialog} onOpenChange={setShowLogoutDialog}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("header.logout.tooltip")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("header.logout.description")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              variant="outline"
              onClick={() => void handleLogoutAll()}
            >
              {t("header.logout.everywhere")}
            </AlertDialogAction>
            <AlertDialogAction onClick={() => void handleLogout()}>
              {t("header.logout.confirm")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <div className="text-muted-foreground flex items-center gap-1 text-sm font-medium md:gap-2">
        {restartRequired && (
          <Tooltip delayDuration={700}>
            <TooltipTrigger asChild>
              <Button
                variant="secondary"
                size="icon-sm"
                className="bg-amber-500/15 text-amber-700 hover:bg-amber-500/25 hover:text-amber-800 dark:text-amber-300 dark:hover:bg-amber-500/25"
                onClick={handleGatewayRestart}
                disabled={gwLoading || isRestarting || isStopping || !canStart}
                aria-label={t("header.gateway.action.restart")}
              >
                <IconRefresh className="size-4" />
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {t("header.gateway.restartRequired")}
            </TooltipContent>
          </Tooltip>
        )}

        <GatewayStatusControl />

        <Separator
          className="mx-2 my-2 hidden md:block"
          orientation="vertical"
        />

        <LanguageMenu />
        <ThemeToggle />

        <Separator className="mx-1 my-2 sm:mx-2" orientation="vertical" />

        <Tooltip delayDuration={700}>
          <TooltipTrigger asChild>
            <Button
              variant="ghost"
              size="icon"
              className="size-8"
              onClick={() => setShowLogoutDialog(true)}
              aria-label={t("header.logout.tooltip")}
            >
              <IconLogout className="size-4.5" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{t("header.logout.tooltip")}</TooltipContent>
        </Tooltip>
      </div>
    </header>
  )
}
