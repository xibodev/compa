import { createFileRoute } from "@tanstack/react-router"
import * as React from "react"
import { useTranslation } from "react-i18next"

import {
  postLauncherDashboardLogin,
  postLauncherDashboardSetup,
} from "@/api/launcher-auth"
import { AuthShell } from "@/components/auth/auth-shell"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { navigateTo } from "@/lib/navigate"
import {
  MIN_PASSWORD_LENGTH,
  validateSetupPassword,
} from "@/lib/password-rules"

function LauncherSetupPage() {
  const { t } = useTranslation()
  const [password, setPassword] = React.useState("")
  const [confirm, setConfirm] = React.useState("")
  // Field errors show once a submit was attempted, then follow the edits.
  const [attempted, setAttempted] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState("")
  const passwordRef = React.useRef<HTMLInputElement>(null)
  const confirmRef = React.useRef<HTMLInputElement>(null)

  const errors = attempted ? validateSetupPassword(password, confirm) : {}
  const passwordError =
    errors.password === "required"
      ? t("launcherSetup.errorRequired")
      : errors.password === "tooShort"
        ? t("launcherSetup.errorTooShort", { min: MIN_PASSWORD_LENGTH })
        : ""
  const confirmError =
    errors.confirm === "required"
      ? t("launcherSetup.errorConfirmRequired")
      : errors.confirm === "mismatch"
        ? t("launcherSetup.errorMismatch")
        : ""

  const onSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setAttempted(true)
    setError("")
    const found = validateSetupPassword(password, confirm)
    if (found.password) {
      passwordRef.current?.focus()
      return
    }
    if (found.confirm) {
      confirmRef.current?.focus()
      return
    }
    setSubmitting(true)
    try {
      const result = await postLauncherDashboardSetup(password, confirm)
      if (!result.ok) {
        setError(result.error)
        setSubmitting(false)
        return
      }
      // The password was just chosen here, so asking for it again right
      // away would only be friction: sign in with it and open the app.
      const login = await postLauncherDashboardLogin(password)
      navigateTo(login.ok ? "/" : "/launcher-login")
    } catch {
      setError(t("launcherSetup.errorNetwork"))
      setSubmitting(false)
    }
  }

  return (
    <AuthShell
      title={t("launcherSetup.title")}
      description={t("launcherSetup.description")}
    >
      <form className="flex flex-col gap-4" onSubmit={onSubmit} noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="setup-password">
            {t("launcherSetup.passwordLabel")}
          </Label>
          <Input
            ref={passwordRef}
            id="setup-password"
            name="password"
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("launcherSetup.passwordPlaceholder", {
              min: MIN_PASSWORD_LENGTH,
            })}
            aria-invalid={passwordError ? true : undefined}
            aria-describedby={
              passwordError ? "setup-password-error" : undefined
            }
          />
          {passwordError && (
            <p id="setup-password-error" className="text-destructive text-sm">
              {passwordError}
            </p>
          )}
        </div>
        <div className="flex flex-col gap-2">
          <Label htmlFor="setup-confirm">
            {t("launcherSetup.confirmLabel")}
          </Label>
          <Input
            ref={confirmRef}
            id="setup-confirm"
            name="confirm"
            type="password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            placeholder={t("launcherSetup.confirmPlaceholder")}
            aria-invalid={confirmError ? true : undefined}
            aria-describedby={confirmError ? "setup-confirm-error" : undefined}
          />
          {confirmError && (
            <p id="setup-confirm-error" className="text-destructive text-sm">
              {confirmError}
            </p>
          )}
        </div>
        <Button type="submit" disabled={submitting}>
          {submitting ? t("labels.loading") : t("launcherSetup.submit")}
        </Button>
        {error ? (
          <p className="text-destructive text-sm" role="alert">
            {error}
          </p>
        ) : null}
      </form>
    </AuthShell>
  )
}

export const Route = createFileRoute("/launcher-setup")({
  component: LauncherSetupPage,
})
