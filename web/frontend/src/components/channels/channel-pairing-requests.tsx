import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type PairingRequest,
  decidePairingRequest,
  getPairingRequests,
} from "@/api/channels"
import { Button } from "@/components/ui/button"

interface ChannelPairingRequestsProps {
  channelName: string
  /** Called with the allow_from entry an approval added. */
  onApproved: (senderID: string) => void
}

/** The senders the channel holds for approval; shown only when there are any. */
export function ChannelPairingRequests({
  channelName,
  onApproved,
}: ChannelPairingRequestsProps) {
  const { t } = useTranslation()
  const [requests, setRequests] = useState<PairingRequest[]>([])
  const [pendingSender, setPendingSender] = useState("")
  const [error, setError] = useState("")

  const load = useCallback(async () => {
    try {
      setRequests(await getPairingRequests(channelName))
    } catch {
      // Without the list there is nothing to approve; the page still works.
      setRequests([])
    }
  }, [channelName])

  useEffect(() => {
    void load()
  }, [load])

  const decide = async (senderID: string, decision: "approve" | "deny") => {
    setPendingSender(senderID)
    setError("")
    try {
      await decidePairingRequest(channelName, senderID, decision)
      if (decision === "approve") onApproved(senderID)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setPendingSender("")
      await load()
    }
  }

  if (requests.length === 0 && !error) return null

  return (
    <div className="bg-card text-card-foreground border-border/60 rounded-xl border px-6 py-4 shadow-sm">
      <p className="text-sm font-medium">{t("channels.pairing.title")}</p>
      <ul className="divide-border/60 mt-2 divide-y">
        {requests.map((request) => (
          <li
            key={request.sender_id}
            className="flex items-center justify-between gap-3 py-2"
          >
            <div className="min-w-0">
              {request.display_name && (
                <p className="truncate text-sm">{request.display_name}</p>
              )}
              <p className="text-muted-foreground truncate font-mono text-xs">
                {request.sender_id}
              </p>
            </div>
            <div className="flex shrink-0 gap-2">
              <Button
                variant="outline"
                size="sm"
                disabled={pendingSender !== ""}
                onClick={() => void decide(request.sender_id, "deny")}
              >
                {t("channels.pairing.deny")}
              </Button>
              <Button
                size="sm"
                disabled={pendingSender !== ""}
                onClick={() => void decide(request.sender_id, "approve")}
              >
                {t("channels.pairing.approve")}
              </Button>
            </div>
          </li>
        ))}
      </ul>
      {error && <p className="text-destructive mt-2 text-sm">{error}</p>}
    </div>
  )
}
