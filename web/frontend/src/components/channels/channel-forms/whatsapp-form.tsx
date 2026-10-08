import {
  IconCheck,
  IconLoader2,
  IconQrcode,
  IconRefresh,
  IconX,
} from "@tabler/icons-react"
import { useEffect, useEffectEvent, useState } from "react"
import { useTranslation } from "react-i18next"

import {
  type ChannelConfig,
  type WhatsAppLinkResponse,
  getWhatsAppLink,
  startWhatsAppLink,
} from "@/api/channels"
import {
  type ArrayFieldFlusher,
  ChannelArrayListField,
} from "@/components/channels/channel-array-list-field"
import {
  asStringArray,
  parseAllowFromInput,
} from "@/components/channels/channel-array-utils"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"

const POLL_INTERVAL_MS = 2000

interface WhatsAppFormProps {
  config: ChannelConfig
  onChange: (key: string, value: unknown) => void
  /** The account got linked; the server enabled the channel. */
  onLinked?: () => void
  registerArrayFieldFlusher?: (
    fieldPath: string,
    flusher: ArrayFieldFlusher | null,
  ) => void
  arrayFieldResetVersion?: number
}

function failed(e: unknown): WhatsAppLinkResponse {
  return { status: "failed", error: e instanceof Error ? e.message : "" }
}

export function WhatsAppForm({
  config,
  onChange,
  onLinked,
  registerArrayFieldFlusher,
  arrayFieldResetVersion,
}: WhatsAppFormProps) {
  const { t } = useTranslation()
  const [link, setLink] = useState<WhatsAppLinkResponse | null>(null)
  const [starting, setStarting] = useState(false)
  const status = link?.status

  const showLink = (next: WhatsAppLinkResponse) => {
    setLink(next)
    if (next.status === "linked") onLinked?.()
  }
  const onPolled = useEffectEvent(showLink)

  useEffect(() => {
    let active = true
    getWhatsAppLink()
      .then((next) => active && setLink(next))
      .catch((e: unknown) => active && setLink(failed(e)))
    return () => {
      active = false
    }
  }, [])

  // Poll while the QR code waits for a scan: it changes about every 20s.
  useEffect(() => {
    if (status !== "waiting") return
    let active = true
    const timer = setInterval(() => {
      getWhatsAppLink()
        .then((next) => active && onPolled(next))
        .catch(() => {
          // Transient network error: keep polling.
        })
    }, POLL_INTERVAL_MS)
    return () => {
      active = false
      clearInterval(timer)
    }
  }, [status])

  const handleLink = async () => {
    setStarting(true)
    try {
      showLink(await startWhatsAppLink())
    } catch (e) {
      setLink(failed(e))
    } finally {
      setStarting(false)
    }
  }

  const renderLinkSection = () => {
    if (!link || starting) {
      return (
        <div className="flex justify-center py-8">
          <IconLoader2
            className="text-muted-foreground animate-spin"
            size={32}
          />
        </div>
      )
    }

    if (link.status === "linked") {
      return (
        <div className="flex justify-center py-6">
          <div className="flex items-center gap-2 rounded-full bg-emerald-500/10 px-4 py-2 text-sm font-medium text-emerald-600 dark:text-emerald-400">
            <IconCheck size={16} />
            {t("channels.whatsapp.linked", { phone: link.phone ?? "" })}
          </div>
        </div>
      )
    }

    if (link.status === "waiting") {
      return (
        <div className="flex flex-col items-center gap-4 py-4">
          {link.qr_data_uri ? (
            <img
              src={link.qr_data_uri}
              alt={t("channels.whatsapp.qrAlt")}
              className="border-border/60 h-48 w-48 rounded-xl border bg-white p-2 shadow-sm"
            />
          ) : (
            <div className="border-border/60 bg-muted flex h-48 w-48 items-center justify-center rounded-xl border">
              <IconLoader2
                className="text-muted-foreground animate-spin"
                size={32}
              />
            </div>
          )}
          <p className="text-muted-foreground max-w-sm px-6 text-center text-sm">
            {t("channels.whatsapp.scanHint")}
          </p>
        </div>
      )
    }

    if (link.status === "failed") {
      return (
        <div className="flex flex-col items-center gap-4 py-6">
          <div className="bg-destructive/10 flex h-14 w-14 items-center justify-center rounded-full">
            <IconX size={28} className="text-destructive" />
          </div>
          <p className="text-destructive px-6 text-center text-sm">
            {link.error || t("channels.whatsapp.errorGeneric")}
          </p>
          <Button variant="outline" onClick={handleLink} className="gap-2">
            <IconRefresh size={14} />
            {t("channels.whatsapp.retry")}
          </Button>
        </div>
      )
    }

    return (
      <div className="flex flex-col items-center gap-4 py-6">
        <p className="text-muted-foreground text-sm">
          {t("channels.whatsapp.notLinked")}
        </p>
        <Button onClick={handleLink} className="gap-2">
          <IconQrcode size={16} />
          {t("channels.whatsapp.link")}
        </Button>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <Card className="shadow-sm">
        <CardHeader className="border-border/60 border-b px-6">
          <CardTitle className="text-foreground text-sm font-medium">
            {t("channels.whatsapp.linkTitle")}
          </CardTitle>
          <CardDescription>{t("channels.whatsapp.linkDesc")}</CardDescription>
        </CardHeader>
        <CardContent className="p-0">{renderLinkSection()}</CardContent>
      </Card>

      <Card className="shadow-sm">
        <CardContent className="divide-border/60 divide-y px-6 py-0 [&>div]:py-5">
          <ChannelArrayListField
            label={t("channels.field.allowFrom")}
            hint={t("channels.whatsapp.allowFromDesc")}
            value={asStringArray(config.allow_from)}
            onChange={(value) => onChange("allow_from", value)}
            placeholder="15550003333"
            parser={parseAllowFromInput}
            fieldPath="allow_from"
            registerFlusher={registerArrayFieldFlusher}
            resetVersion={arrayFieldResetVersion}
          />
        </CardContent>
      </Card>
    </div>
  )
}
