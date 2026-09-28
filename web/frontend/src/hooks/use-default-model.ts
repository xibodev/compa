import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { getDefaultModel, setDefaultModel } from "@/api/default-model"
import { showSaveSuccessOrRestartToast } from "@/lib/restart-required"
import { refreshGatewayState } from "@/store/gateway"

/**
 * The configured default model selection. `selection` is empty both when no
 * default is set and while the default is not known yet; `loaded` tells them
 * apart. A failed load keeps the last known selection and reports `error`.
 */
export function useDefaultModel() {
  const { t } = useTranslation()
  const [selection, setSelection] = useState("")
  const [loaded, setLoaded] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState("")
  // The latest read or write owns the state; an older response is dropped.
  const sequence = useRef(0)
  const pendingSaves = useRef(0)

  const refresh = useCallback(async () => {
    const id = ++sequence.current
    setLoading(true)
    try {
      const result = await getDefaultModel()
      if (id !== sequence.current) return
      setSelection(result.selection)
      setLoaded(true)
      setError("")
    } catch (cause) {
      if (id !== sequence.current) return
      setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (id === sequence.current) setLoading(false)
    }
  }, [])

  /**
   * Sets the default model, or clears it when `next` is empty, and reports
   * the outcome in a toast. Resolves to whether the server accepted it.
   */
  const setDefault = useCallback(
    async (next: string) => {
      const id = ++sequence.current
      pendingSaves.current += 1
      setSaving(true)
      try {
        const result = await setDefaultModel(next)
        if (id === sequence.current) {
          setSelection(result.selection)
          setLoaded(true)
          setError("")
        }
        const gateway = await refreshGatewayState({ force: true })
        showSaveSuccessOrRestartToast(
          t,
          result.selection
            ? t("models.management.default.saved")
            : t("models.management.default.cleared"),
          t("models.management.default.configName"),
          gateway?.restartRequired === true,
        )
        return true
      } catch (cause) {
        toast.error(
          cause instanceof Error && cause.message
            ? cause.message
            : t("models.management.default.saveFailed"),
        )
        return false
      } finally {
        if (id === sequence.current) setLoading(false)
        pendingSaves.current -= 1
        if (pendingSaves.current === 0) setSaving(false)
      }
    },
    [t],
  )

  useEffect(() => {
    void refresh()
    return () => {
      sequence.current += 1
    }
  }, [refresh])

  return { selection, loaded, loading, saving, error, refresh, setDefault }
}

export type DefaultModelState = ReturnType<typeof useDefaultModel>
