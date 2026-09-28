import { useTranslation } from "react-i18next"

import { VoiceSettings } from "@/components/config/voice-settings"
import { PageHeader } from "@/components/page-header"

/** Voice has a page of its own rather than a card deep inside Config. */
export function VoicePage() {
  const { t } = useTranslation()
  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.voice")} />
      <div className="flex-1 overflow-auto p-3 lg:p-6">
        <div className="mx-auto w-full max-w-[1000px] space-y-4">
          <p className="text-muted-foreground max-w-2xl text-sm">
            {t("voice.pageDescription")}
          </p>
          <VoiceSettings />
        </div>
      </div>
    </div>
  )
}
