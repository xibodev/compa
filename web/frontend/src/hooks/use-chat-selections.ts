import { useCallback, useEffect, useState } from "react"

import {
  type ModelRoute,
  type ProviderTarget,
  listModelRoutes,
  listProviderTargets,
} from "@/api/provider-instances"

export function useChatSelections() {
  const [targets, setTargets] = useState<ProviderTarget[]>([])
  const [routes, setRoutes] = useState<ModelRoute[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState("")

  const refresh = useCallback(async () => {
    setLoading(true)
    try {
      const [targetResult, routeResult] = await Promise.allSettled([
        listProviderTargets(),
        listModelRoutes(),
      ])
      if (targetResult.status === "fulfilled")
        setTargets(targetResult.value.targets || [])
      else setTargets([])
      if (routeResult.status === "fulfilled")
        setRoutes(routeResult.value.routes || [])
      else setRoutes([])
      const errors = [targetResult, routeResult]
        .filter((result) => result.status === "rejected")
        .map((result) =>
          result.status === "rejected" && result.reason instanceof Error
            ? result.reason.message
            : "Failed to load models",
        )
      setError(errors.join("; "))
    } catch {
      setError("Failed to load models")
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  return { targets, routes, loading, error, refresh }
}
