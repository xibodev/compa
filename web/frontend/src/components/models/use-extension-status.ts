import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import { type ExtensionStatus, getExtensionStatus } from "@/api/extension"

/** The extension's connection and the providers it serves. */
export interface ExtensionStatusState {
  /** Null until the first load answers. */
  status: ExtensionStatus | null
  /** Why the last load failed, else "". */
  error: string
  reload: () => Promise<void>
  /** Shows a status the server just returned, e.g. after connecting. */
  setStatus: (status: ExtensionStatus) => void
}

/**
 * Loads the extension status once for the Models page, whose provider cards
 * sign in to the providers it serves and whose Extension section connects it.
 */
export function useExtensionStatus(): ExtensionStatusState {
  const { t } = useTranslation()
  const [status, setStatusValue] = useState<ExtensionStatus | null>(null)
  const [error, setError] = useState("")
  // Only the latest request may set the status.
  const sequence = useRef(0)

  const reload = useCallback(async () => {
    const request = ++sequence.current
    try {
      const next = await getExtensionStatus()
      if (request !== sequence.current) return
      setStatusValue(next)
      setError("")
    } catch (cause) {
      if (request !== sequence.current) return
      setError(
        cause instanceof Error && cause.message
          ? cause.message
          : t("models.extension.errors.load"),
      )
    }
  }, [t])

  const setStatus = useCallback((next: ExtensionStatus) => {
    sequence.current += 1
    setStatusValue(next)
    setError("")
  }, [])

  useEffect(() => {
    void reload()
    return () => {
      sequence.current += 1
    }
  }, [reload])

  return useMemo(
    () => ({ status, error, reload, setStatus }),
    [status, error, reload, setStatus],
  )
}
