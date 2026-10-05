import {
  IconCopy,
  IconExternalLink,
  IconKey,
  IconLoader2,
  IconLogin,
  IconLogout,
} from "@tabler/icons-react"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import {
  type ExtensionProvider,
  type ExtensionSignInMethod,
  type SignInFlow,
  completeExtensionSignIn,
  pollExtensionSignIn,
  removeExtensionCredential,
  saveExtensionToken,
  startExtensionSignIn,
} from "@/api/extension"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { safeExternalURL } from "@/lib/safe-url"

const errorText = (cause: unknown, fallback: string) =>
  cause instanceof Error && cause.message ? cause.message : fallback

interface ExtensionProviderActionsProps {
  provider: ExtensionProvider
  /** Called after a sign-in, a sign-out or a token change. */
  onChanged: () => void | Promise<void>
}

/**
 * The sign-in controls of a provider the extension serves: Sign in or Sign
 * out for an account, Paste, Replace or Remove token for a token, and none
 * for a provider that needs no key.
 */
export function ExtensionProviderActions({
  provider,
  onChanged,
}: ExtensionProviderActionsProps) {
  const { t } = useTranslation()
  const [busy, setBusy] = useState<"" | "token" | "remove">("")
  const [tokenOpen, setTokenOpen] = useState(false)
  const [token, setToken] = useState("")
  const [signingIn, setSigningIn] = useState(false)

  const run = async (
    key: "token" | "remove",
    operation: () => Promise<unknown>,
  ) => {
    setBusy(key)
    try {
      await operation()
      await onChanged()
      return true
    } catch (cause) {
      toast.error(errorText(cause, t("models.extension.errors.request")))
      return false
    } finally {
      setBusy("")
    }
  }

  const remove = () =>
    void run("remove", () => removeExtensionCredential(provider.id))

  if (!provider.supported || provider.credential === "none") return null
  const disabled = busy !== ""

  if (provider.credential === "token") {
    if (tokenOpen) {
      return (
        <form
          className="flex w-full min-w-0 gap-2 sm:max-w-md"
          onSubmit={(event) => {
            event.preventDefault()
            const value = token.trim()
            if (!value) return
            void run("token", () =>
              saveExtensionToken(provider.id, value),
            ).then((ok) => {
              if (!ok) return
              setTokenOpen(false)
              setToken("")
              toast.success(t("models.extension.tokenSaved"))
            })
          }}
        >
          <Input
            type="password"
            autoFocus
            autoComplete="off"
            value={token}
            aria-label={t("models.extension.tokenLabel", {
              name: provider.name,
            })}
            placeholder={t("models.extension.tokenPlaceholder")}
            disabled={disabled}
            onChange={(event) => setToken(event.target.value)}
          />
          <Button size="sm" type="submit" disabled={disabled || !token.trim()}>
            {busy === "token" && (
              <IconLoader2 className="size-4 animate-spin" />
            )}
            {t("models.extension.save")}
          </Button>
          <Button
            size="sm"
            type="button"
            variant="ghost"
            disabled={disabled}
            onClick={() => setTokenOpen(false)}
          >
            {t("models.extension.cancel")}
          </Button>
        </form>
      )
    }
    return (
      <>
        <Button
          size="sm"
          variant={provider.connected ? "outline" : "default"}
          disabled={disabled}
          onClick={() => {
            setToken("")
            setTokenOpen(true)
          }}
        >
          <IconKey className="mr-1 size-4" />
          {provider.connected
            ? t("models.extension.replaceToken")
            : t("models.extension.pasteToken")}
        </Button>
        {provider.connected && (
          <Button
            size="sm"
            variant="ghost"
            disabled={disabled}
            onClick={remove}
          >
            {busy === "remove" && (
              <IconLoader2 className="size-4 animate-spin" />
            )}
            {t("models.extension.removeToken")}
          </Button>
        )}
      </>
    )
  }

  return (
    <>
      {provider.connected ? (
        <Button
          size="sm"
          variant="outline"
          disabled={disabled}
          onClick={remove}
        >
          {busy === "remove" ? (
            <IconLoader2 className="mr-1 size-4 animate-spin" />
          ) : (
            <IconLogout className="mr-1 size-4" />
          )}
          {t("models.extension.signOut")}
        </Button>
      ) : (
        <Button
          size="sm"
          disabled={disabled}
          onClick={() => setSigningIn(true)}
        >
          <IconLogin className="mr-1 size-4" />
          {t("models.extension.signIn")}
        </Button>
      )}
      {signingIn && (
        <SignInDialog
          provider={provider}
          onClose={() => setSigningIn(false)}
          onApproved={async () => {
            setSigningIn(false)
            toast.success(t("models.extension.signInSuccess"))
            await onChanged()
          }}
        />
      )}
    </>
  )
}

/**
 * Signs in to a provider the extension serves: with a device code approved
 * on another page, or with a code pasted back from the provider's sign-in
 * page.
 */
export function SignInDialog({
  provider,
  onClose,
  onApproved,
}: {
  provider: ExtensionProvider
  onClose: () => void
  onApproved: () => void | Promise<void>
}) {
  const { t } = useTranslation()
  const methods: ExtensionSignInMethod[] =
    provider.methods && provider.methods.length > 0
      ? provider.methods
      : ["device"]
  const [method, setMethod] = useState<ExtensionSignInMethod>(
    methods.includes("device") ? "device" : methods[0],
  )
  const [flow, setFlow] = useState<SignInFlow | null>(null)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState("")
  const [code, setCode] = useState("")
  const [completing, setCompleting] = useState(false)
  const approvedRef = useRef(onApproved)
  useEffect(() => {
    approvedRef.current = onApproved
  }, [onApproved])

  const start = async () => {
    setStarting(true)
    setError("")
    try {
      setFlow(await startExtensionSignIn(provider.id, method))
    } catch (cause) {
      setError(errorText(cause, t("models.extension.errors.request")))
    } finally {
      setStarting(false)
    }
  }

  useEffect(() => {
    if (!flow || flow.method !== "device") return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let interval = Math.max(flow.interval_seconds ?? 0, 2)
    const deadline = flow.expires_at ? Date.parse(flow.expires_at) : NaN

    const tick = async () => {
      if (cancelled) return
      if (!Number.isNaN(deadline) && Date.now() >= deadline) {
        setError(t("models.extension.errors.expired"))
        return
      }
      try {
        const result = await pollExtensionSignIn(flow.flow_id)
        if (cancelled) return
        if (result.status === "approved") {
          await approvedRef.current()
          return
        }
        if (result.status === "denied" || result.status === "expired") {
          setError(
            result.error || t(`models.extension.errors.${result.status}`),
          )
          return
        }
        if (result.status === "slow_down") interval += 5
      } catch (cause) {
        if (cancelled) return
        setError(errorText(cause, t("models.extension.errors.request")))
        return
      }
      timer = setTimeout(() => void tick(), interval * 1000)
    }
    timer = setTimeout(() => void tick(), interval * 1000)
    return () => {
      cancelled = true
      if (timer) clearTimeout(timer)
    }
  }, [flow, t])

  const complete = async () => {
    if (!flow) return
    setCompleting(true)
    setError("")
    try {
      await completeExtensionSignIn(flow.flow_id, code.trim())
      await onApproved()
    } catch (cause) {
      setError(errorText(cause, t("models.extension.errors.request")))
    } finally {
      setCompleting(false)
    }
  }

  const verifyURL = safeExternalURL(
    flow?.verification_uri_complete || flow?.verification_uri,
  )
  const authorizationURL = safeExternalURL(flow?.authorization_url)

  return (
    <Dialog open onOpenChange={(open) => !open && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>
            {t("models.extension.signInTitle", { name: provider.name })}
          </DialogTitle>
          <DialogDescription>
            {t("models.extension.signInDescription")}
          </DialogDescription>
        </DialogHeader>

        {!flow && (
          <div className="space-y-3">
            {methods.length > 1 && (
              <div
                className="flex gap-2"
                role="radiogroup"
                aria-label={t("models.extension.methodLabel")}
              >
                {methods.map((item) => (
                  <Button
                    key={item}
                    type="button"
                    size="sm"
                    role="radio"
                    aria-checked={method === item}
                    variant={method === item ? "default" : "outline"}
                    onClick={() => setMethod(item)}
                  >
                    {t(`models.extension.method.${item}`)}
                  </Button>
                ))}
              </div>
            )}
            <Button onClick={() => void start()} disabled={starting}>
              {starting && <IconLoader2 className="size-4 animate-spin" />}
              {t("models.extension.startSignIn")}
            </Button>
          </div>
        )}

        {flow?.method === "device" && (
          <div className="space-y-3">
            <p className="text-sm">
              {t("models.extension.deviceInstructions")}
            </p>
            {flow.user_code && (
              <div className="flex items-center gap-2">
                <code
                  data-testid="extension-user-code"
                  className="bg-muted rounded-md px-3 py-2 font-mono text-2xl tracking-widest"
                >
                  {flow.user_code}
                </code>
                <Button
                  size="icon-sm"
                  variant="ghost"
                  aria-label={t("models.extension.copyCode")}
                  onClick={() => {
                    void navigator.clipboard
                      ?.writeText(flow.user_code ?? "")
                      .then(() => toast.success(t("models.extension.copied")))
                      .catch(() => undefined)
                  }}
                >
                  <IconCopy className="size-4" />
                </Button>
              </div>
            )}
            {verifyURL && (
              <a
                href={verifyURL}
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary inline-flex items-center gap-1 text-sm underline"
              >
                {t("models.extension.openVerification")}
                <IconExternalLink className="size-4" />
              </a>
            )}
            {!error && (
              <p className="text-muted-foreground flex items-center gap-2 text-xs">
                <IconLoader2 className="size-3 animate-spin" />
                {t("models.extension.waiting")}
              </p>
            )}
          </div>
        )}

        {flow?.method === "manual" && (
          <form
            className="space-y-3"
            onSubmit={(event) => {
              event.preventDefault()
              if (code.trim()) void complete()
            }}
          >
            {authorizationURL && (
              <a
                href={authorizationURL}
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary inline-flex items-center gap-1 text-sm underline"
              >
                {t("models.extension.openAuthorization")}
                <IconExternalLink className="size-4" />
              </a>
            )}
            <div className="space-y-2">
              <Label htmlFor="extension-signin-code">
                {t("models.extension.codeLabel")}
              </Label>
              <Input
                id="extension-signin-code"
                value={code}
                onChange={(event) => setCode(event.target.value)}
              />
            </div>
            <Button type="submit" disabled={completing || !code.trim()}>
              {completing && <IconLoader2 className="size-4 animate-spin" />}
              {t("models.extension.submitCode")}
            </Button>
          </form>
        )}

        {error && (
          <p role="alert" className="text-destructive text-sm">
            {error}
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}
