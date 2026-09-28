import { createFileRoute } from "@tanstack/react-router"
import * as React from "react"
import { useTranslation } from "react-i18next"

import {
  getLauncherAuthStatus,
  postLauncherDashboardLogin,
} from "@/api/launcher-auth"
import { AuthShell } from "@/components/auth/auth-shell"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { navigateTo } from "@/lib/navigate"

function LauncherLoginPage() {
  const { t } = useTranslation()
  const [password, setPassword] = React.useState("")
  const [attempted, setAttempted] = React.useState(false)
  const [submitting, setSubmitting] = React.useState(false)
  const [error, setError] = React.useState("")
  const passwordRef = React.useRef<HTMLInputElement>(null)

  // If the password store has never been initialized, go to setup instead.
  React.useEffect(() => {
    void getLauncherAuthStatus()
      .then((s) => {
        if (!s.initialized) {
          navigateTo("/launcher-setup")
        }
      })
      .catch(() => {
        /* network error — stay on login page */
      })
  }, [])

  const missingPassword = attempted && !password.trim()

  const loginWithPassword = React.useCallback(
    async (passwordValue: string) => {
      setError("")
      setSubmitting(true)
      try {
        const result = await postLauncherDashboardLogin(passwordValue)
        if (result.ok) {
          navigateTo("/")
          return
        }
        if (result.status === 409) {
          navigateTo("/launcher-setup")
          return
        }
        if (result.status === 401) {
          setError(t("launcherLogin.errorInvalid"))
        } else {
          setError(result.error)
        }
        setSubmitting(false)
      } catch {
        setError(t("launcherLogin.errorNetwork"))
        setSubmitting(false)
      }
    },
    [t],
  )

  const onSubmit = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    setAttempted(true)
    if (!password.trim()) {
      setError("")
      passwordRef.current?.focus()
      return
    }
    await loginWithPassword(password)
  }

  return (
    <AuthShell
      title={t("launcherLogin.title")}
      description={t("launcherLogin.description")}
    >
      <form className="flex flex-col gap-4" onSubmit={onSubmit} noValidate>
        <div className="flex flex-col gap-2">
          <Label htmlFor="launcher-password">
            {t("launcherLogin.passwordLabel")}
          </Label>
          <Input
            ref={passwordRef}
            id="launcher-password"
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder={t("launcherLogin.passwordPlaceholder")}
            aria-invalid={missingPassword ? true : undefined}
            aria-describedby={
              missingPassword ? "launcher-password-error" : undefined
            }
          />
          {missingPassword && (
            <p
              id="launcher-password-error"
              className="text-destructive text-sm"
            >
              {t("launcherLogin.errorRequired")}
            </p>
          )}
        </div>
        <Button type="submit" disabled={submitting}>
          {submitting ? t("labels.loading") : t("launcherLogin.submit")}
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

export const Route = createFileRoute("/launcher-login")({
  component: LauncherLoginPage,
})
