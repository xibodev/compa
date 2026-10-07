import { useId } from "react"
import { useTranslation } from "react-i18next"

import type { ChannelConfig } from "@/api/channels"
import {
  DM_POLICIES,
  GROUP_POLICIES,
  WHATSAPP_CHATS,
  effectiveDMPolicy,
  effectiveGroupPolicy,
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

interface OptionSelectProps<T extends string> {
  label: string
  value: T
  options: readonly T[]
  optionLabel: (option: T) => string
  onChange: (value: T) => void
}

function OptionSelect<T extends string>({
  label,
  value,
  options,
  optionLabel,
  onChange,
}: OptionSelectProps<T>) {
  const id = useId()
  return (
    <Field label={label} htmlFor={id}>
      <Select value={value} onValueChange={(next) => onChange(next as T)}>
        <SelectTrigger id={id} className="w-full">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map((option) => (
            <SelectItem key={option} value={option}>
              {optionLabel(option)}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </Field>
  )
}

interface ChannelAccessFieldsProps {
  config: ChannelConfig
  onChange: (key: string, value: unknown) => void
  /** Native WhatsApp also chooses which chats are input. */
  showChats?: boolean
}

/** Who may talk to the channel: its DM and group policies. */
export function ChannelAccessFields({
  config,
  onChange,
  showChats = false,
}: ChannelAccessFieldsProps) {
  const { t } = useTranslation()

  return (
    <>
      <OptionSelect
        label={t("channels.field.dmPolicy")}
        value={effectiveDMPolicy(config)}
        options={DM_POLICIES}
        optionLabel={(option) => t(`channels.policy.${option}`)}
        onChange={(value) => onChange("dm_policy", value)}
      />
      <OptionSelect
        label={t("channels.field.groupPolicy")}
        value={effectiveGroupPolicy(config)}
        options={GROUP_POLICIES}
        optionLabel={(option) => t(`channels.policy.${option}`)}
        onChange={(value) => onChange("group_policy", value)}
      />
      {showChats && (
        <OptionSelect
          label={t("channels.field.chats")}
          value={effectiveWhatsAppChats(config)}
          options={WHATSAPP_CHATS}
          optionLabel={(option) => t(`channels.chats.${option}`)}
          onChange={(value) => onChange("chats", value)}
        />
      )}
    </>
  )
}
