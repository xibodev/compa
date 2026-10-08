import { useId } from "react"
import { useTranslation } from "react-i18next"

import type { ChannelConfig } from "@/api/channels"
import {
  WHATSAPP_CHATS,
  effectiveWhatsAppChats,
} from "@/components/channels/channel-config-fields"
import { Field } from "@/components/shared-form"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"

interface ChannelAccessFieldsProps {
  config: ChannelConfig
  onChange: (key: string, value: unknown) => void
}

/**
 * Native WhatsApp's Chats: which chats it takes as input. Who may talk to a
 * channel is not a choice: it answers only the accounts in Allow From.
 */
export function ChannelAccessFields({
  config,
  onChange,
}: ChannelAccessFieldsProps) {
  const { t } = useTranslation()
  const id = useId()

  return (
    <Field label={t("channels.field.chats")} htmlFor={id}>
      <Select
        value={effectiveWhatsAppChats(config)}
        onValueChange={(value) => onChange("chats", value)}
      >
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {WHATSAPP_CHATS.map((option) => (
            <SelectItem key={option} value={option}>
              {t(`channels.chats.${option}`)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}
