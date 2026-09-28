import { useCallback, useEffect, useRef, useState } from "react"

import {
  type ModelRoute,
  type ProviderInstance,
  type ProviderInstanceCatalog,
  type ProviderRosterEntry,
  type ProviderTarget,
  listModelRoutes,
  listProviderInstanceCatalogs,
  listProviderInstances,
  listProviderRoster,
  listProviderTargets,
} from "@/api/provider-instances"

export function useProviderInstances() {
  const [instances, setInstances] = useState<ProviderInstance[]>([])
  const [catalogs, setCatalogs] = useState<ProviderInstanceCatalog[]>([])
  const [targets, setTargets] = useState<ProviderTarget[]>([])
  const [routes, setRoutes] = useState<ModelRoute[]>([])
  const [roster, setRoster] = useState<ProviderRosterEntry[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const requestSequence = useRef(0)

  const refresh = useCallback(async () => {
    const requestID = ++requestSequence.current
    setLoading(true)
    try {
      const [instanceData, catalogData, targetData, routeData, rosterData] =
        await Promise.all([
          listProviderInstances(),
          listProviderInstanceCatalogs(),
          listProviderTargets(true),
          listModelRoutes(),
          listProviderRoster(),
        ])
      if (requestID !== requestSequence.current) return
      setInstances(instanceData.instances || [])
      setCatalogs(catalogData.catalogs || [])
      setTargets(targetData.targets || [])
      setRoutes(routeData.routes || [])
      setRoster(rosterData.providers || [])
      setError("")
    } catch (cause) {
      if (requestID !== requestSequence.current) return
      setError(
        cause instanceof Error
          ? cause.message
          : "Failed to load provider instances",
      )
    } finally {
      if (requestID === requestSequence.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void refresh()
    return () => {
      requestSequence.current += 1
    }
  }, [refresh])
  return {
    instances,
    catalogs,
    targets,
    routes,
    roster,
    loading,
    error,
    refresh,
  }
}
