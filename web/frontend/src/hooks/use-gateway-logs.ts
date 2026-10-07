import { useAtomValue } from "jotai"
import { useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { clearGatewayLogs, getGatewayLogs } from "@/api/gateway"
import { gatewayAtom } from "@/store/gateway"

const MAX_VISIBLE_LOG_LINES = 5000

/** A log line with its position in the run, which keys it while it is shown. */
export interface GatewayLogLine {
  offset: number
  text: string
}

function retainLatestLogs(logs: GatewayLogLine[]) {
  return logs.length > MAX_VISIBLE_LOG_LINES
    ? logs.slice(-MAX_VISIBLE_LOG_LINES)
    : logs
}

/** Numbers lines that end at total, the run's line count after them. */
function numberLines(lines: string[], total: number): GatewayLogLine[] {
  const first = total - lines.length
  return lines.map((text, index) => ({ offset: first + index, text }))
}

export function useGatewayLogs() {
  const { t } = useTranslation()
  const [logs, setLogs] = useState<GatewayLogLine[]>([])
  const [clearing, setClearing] = useState(false)
  const logOffsetRef = useRef(0)
  const logRunIdRef = useRef(-1)
  const syncTokenRef = useRef(0)

  const gateway = useAtomValue(gatewayAtom)

  const clearLogs = async () => {
    setClearing(true)
    try {
      const data = await clearGatewayLogs()
      syncTokenRef.current += 1
      setLogs([])
      logOffsetRef.current = data.log_total ?? 0
      if (data.log_run_id !== undefined) {
        logRunIdRef.current = data.log_run_id
      }
    } catch (cause) {
      console.error("Failed to clear logs:", cause)
      toast.error(t("pages.logs.clear_error"))
    } finally {
      setClearing(false)
    }
  }

  useEffect(() => {
    let mounted = true
    let timeout: ReturnType<typeof setTimeout>

    const fetchLogs = async () => {
      if (
        !mounted ||
        !["running", "starting", "restarting", "stopping"].includes(
          gateway.status,
        )
      ) {
        if (mounted) {
          timeout = setTimeout(fetchLogs, 1000)
        }
        return
      }

      try {
        const requestToken = syncTokenRef.current
        const requestOffset = logOffsetRef.current
        const requestRunId = logRunIdRef.current
        const data = await getGatewayLogs({
          log_offset: requestOffset,
          log_run_id: requestRunId,
        })

        if (!mounted || requestToken !== syncTokenRef.current) {
          return
        }

        if (data.log_run_id !== undefined && data.log_run_id !== requestRunId) {
          logRunIdRef.current = data.log_run_id
          logOffsetRef.current = 0
          if (data.logs) {
            const total = data.log_total || data.logs.length
            setLogs(retainLatestLogs(numberLines(data.logs, total)))
            logOffsetRef.current = total
          }
        } else if (data.logs && data.logs.length > 0) {
          const total = data.log_total || requestOffset + data.logs.length
          const nextLogs = numberLines(data.logs, total)
          setLogs((prev) => retainLatestLogs([...prev, ...nextLogs]))
          logOffsetRef.current = total
        }
      } catch {
        // Ignore simple fetch errors during polling.
      } finally {
        if (mounted) {
          timeout = setTimeout(
            fetchLogs,
            document.visibilityState === "hidden" ? 10_000 : 1000,
          )
        }
      }
    }

    fetchLogs()

    return () => {
      mounted = false
      clearTimeout(timeout)
    }
  }, [gateway.status])

  return {
    clearLogs,
    clearing,
    logs,
  }
}
