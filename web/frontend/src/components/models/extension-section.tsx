import { IconLoader2, IconPlugConnected } from "@tabler/icons-react"
import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  DEFAULT_EXTENSION_URL,
  type ExtensionStatus,
  configureExtension,
  disconnectExtension,
  extensionProviderState,
} from "@/api/extension"
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
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { cn } from "@/lib/utils"

import type { ExtensionStatusState } from "./use-extension-status"

const errorText = (cause: unknown, fallback: string) =>
  cause instanceof Error && cause.message ? cause.message : fallback

interface ExtensionSectionProps {
  /** The extension status the Models page loads. */
  extension: ExtensionStatusState
  /** Called after the connection changes so the provider list reloads. */
  onChanged?: () => void | Promise<void>
}

const PROVIDER_STATE_CLASS = {
  ready: "text-emerald-600 dark:text-emerald-400",
  connected: "text-emerald-600 dark:text-emerald-400",
  not_ready: "text-amber-600 dark:text-amber-400",
  needs_token: "text-amber-600 dark:text-amber-400",
  needs_sign_in: "text-amber-600 dark:text-amber-400",
  unsupported: "text-muted-foreground",
} as const

/**
 * The extension: a local companion app that serves more providers. It is
 * connected here; each provider it serves has a card in the provider list,
 * where it is signed in to, so this section only names them and their state.
 */
export function ExtensionSection({
  extension,
  onChanged,
}: ExtensionSectionProps) {
  const { t } = useTranslation()
  const { status, error: loadError, reload, setStatus } = extension
  const [configOpen, setConfigOpen] = useState(false)
  const [confirmDisconnect, setConfirmDisconnect] = useState(false)
  const [disconnecting, setDisconnecting] = useState(false)

  const disconnect = async () => {
    setDisconnecting(true)
    try {
      await disconnectExtension()
      await reload()
      await onChanged?.()
    } catch (cause) {
      toast.error(errorText(cause, t("models.extension.errors.request")))
    } finally {
      setDisconnecting(false)
    }
  }

  const state = status?.status ?? "not_configured"
  const providers = status?.providers ?? []

  return (
    <section
      className="bg-card space-y-3 rounded-xl border p-4"
      aria-label={t("models.extension.title")}
    >
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h3 className="flex items-center gap-2 text-lg font-semibold">
            <IconPlugConnected className="size-5" />
            {t("models.extension.title")}
          </h3>
          <p className="text-muted-foreground text-sm">
            {t("models.extension.description")}
          </p>
          <p
            className={cn(
              "text-xs break-all",
              state === "connected"
                ? "text-emerald-600 dark:text-emerald-400"
                : state === "unreachable"
                  ? "text-destructive"
                  : "text-muted-foreground",
            )}
          >
            {t(`models.extension.status.${state}`)}
            {status?.url ? ` · ${status.url}` : ""}
            {status?.version
              ? ` · ${t("models.extension.version", { version: status.version })}`
              : ""}
          </p>
          {status?.error && (
            <p className="text-destructive text-xs break-words">
              {status.error}
            </p>
          )}
        </div>
        <div className="flex gap-2">
          <Button size="sm" onClick={() => setConfigOpen(true)}>
            {state === "not_configured"
              ? t("models.extension.connect")
              : t("models.extension.edit")}
          </Button>
          {state !== "not_configured" && (
            <Button
              size="sm"
              variant="outline"
              disabled={disconnecting}
              onClick={() => setConfirmDisconnect(true)}
            >
              {disconnecting && <IconLoader2 className="size-4 animate-spin" />}
              {t("models.extension.disconnect")}
            </Button>
          )}
        </div>
      </div>
      {loadError && (
        <p role="alert" className="text-destructive text-sm">
          {loadError}
        </p>
      )}

      {providers.length > 0 && (
        <div className="space-y-2">
          <p className="text-sm font-medium">
            {t("models.extension.providersTitle")}
          </p>
          <ul className="divide-y rounded-lg border text-sm">
            {providers.map((provider) => {
              const providerState = extensionProviderState(provider)
              return (
                <li
                  key={provider.id}
                  aria-label={provider.name}
                  className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 px-3 py-2"
                >
                  <span className="min-w-0 truncate font-medium">
                    {provider.name}
                  </span>
                  <span
                    className={cn(
                      "text-xs",
                      PROVIDER_STATE_CLASS[providerState],
                    )}
                  >
                    {providerState === "unsupported"
                      ? provider.reason ||
                        t("models.extension.state.unsupported")
                      : t(`models.extension.state.${providerState}`)}
                  </span>
                </li>
              )
            })}
          </ul>
          <p className="text-muted-foreground text-xs">
            {t("models.extension.providersHint")}
          </p>
        </div>
      )}

      <ConfigDialog
        open={configOpen}
        status={status}
        onOpenChange={setConfigOpen}
        onSaved={async (next) => {
          setStatus(next)
          setConfigOpen(false)
          toast.success(
            t("models.extension.connectedToast", {
              count: next.providers?.length ?? 0,
            }),
          )
          await onChanged?.()
        }}
      />

      <AlertDialog open={confirmDisconnect} onOpenChange={setConfirmDisconnect}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("models.extension.disconnectTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {t("models.extension.disconnectDescription")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>
              {t("models.extension.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction onClick={() => void disconnect()}>
              {t("models.extension.disconnect")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </section>
  )
}

function ConfigDialog({
  open,
  status,
  onOpenChange,
  onSaved,
}: {
  open: boolean
  status: ExtensionStatus | null
  onOpenChange: (open: boolean) => void
  onSaved: (status: ExtensionStatus) => void | Promise<void>
}) {
  const { t } = useTranslation()
  const [url, setURL] = useState("")
  const [secret, setSecret] = useState("")
  const [error, setError] = useState("")
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (open) {
      // The extension listens on a fixed local address unless told
      // otherwise, so a first connection starts from it.
      setURL(status?.url || DEFAULT_EXTENSION_URL)
      setSecret("")
      setError("")
    }
  }, [open, status?.url])

  const submit = async () => {
    setSaving(true)
    setError("")
    try {
      const payload =
        secret === "" && status?.has_secret
          ? { url: url.trim(), keep_secret: true }
          : { url: url.trim(), secret: secret || undefined }
      await onSaved(await configureExtension(payload))
    } catch (cause) {
      setError(errorText(cause, t("models.extension.errors.request")))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <form
          className="space-y-4"
          onSubmit={(event) => {
            event.preventDefault()
            void submit()
          }}
        >
          <DialogHeader>
            <DialogTitle>{t("models.extension.configTitle")}</DialogTitle>
            <DialogDescription>
              {t("models.extension.configDescription")}
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2">
            <Label htmlFor="extension-url">
              {t("models.extension.urlLabel")}
            </Label>
            <Input
              id="extension-url"
              value={url}
              spellCheck={false}
              placeholder={DEFAULT_EXTENSION_URL}
              onChange={(event) => setURL(event.target.value)}
            />
            <p className="text-muted-foreground text-xs">
              {t("models.extension.urlHint")}
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="extension-secret">
              {t("models.extension.secretLabel")}
            </Label>
            <Input
              id="extension-secret"
              type="password"
              value={secret}
              placeholder={
                status?.has_secret
                  ? t("models.extension.secretKeep")
                  : t("models.extension.secretOptional")
              }
              onChange={(event) => setSecret(event.target.value)}
            />
          </div>
          {error && (
            <p role="alert" className="text-destructive text-sm">
              {error}
            </p>
          )}
          <DialogFooter>
            <Button
              type="button"
              variant="ghost"
              onClick={() => onOpenChange(false)}
            >
              {t("models.extension.cancel")}
            </Button>
            <Button type="submit" disabled={saving || !url.trim()}>
              {saving && <IconLoader2 className="size-4 animate-spin" />}
              {t("models.extension.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
