import {
  IconBrandChrome,
  IconBrandDingtalk,
  IconBrandDiscord,
  IconBrandLine,
  IconBrandMatrix,
  IconBrandQq,
  IconBrandSlack,
  IconBrandTelegram,
  IconBrandWechat,
  IconBrandWhatsapp,
  IconCamera,
  IconMessages,
  IconPlug,
  IconRobot,
} from "@tabler/icons-react"
import type { TFunction } from "i18next"
import { useAtomValue } from "jotai"
import * as React from "react"

import {
  type AppConfig,
  type SupportedChannel,
  getAppConfig,
  getChannelsCatalog,
} from "@/api/channels"
import { getChannelDisplayName } from "@/components/channels/channel-display-name"
import { gatewayAtom } from "@/store/gateway"

const CHANNEL_IMPORTANCE_TAIL = [
  "slack",
  "line",
  "wecom",
  "dingtalk",
  "qq",
  "onebot",
  "matrix",
  "web",
  "maixcam",
  "irc",
  "whatsapp",
  "whatsapp_native",
]

function getChannelImportanceOrder(language: string): string[] {
  const priority = language.startsWith("zh")
    ? ["feishu", "weixin", "discord", "telegram"]
    : ["discord", "telegram", "feishu", "weixin"]
  return [...priority, ...CHANNEL_IMPORTANCE_TAIL]
}

function IconLark({ className }: { className?: string }) {
  return React.createElement("span", {
    className,
    "aria-hidden": "true",
    style: {
      display: "inline-block",
      backgroundColor: "currentColor",
      mask: "url(/lark.svg) center / contain no-repeat",
      WebkitMask: "url(/lark.svg) center / contain no-repeat",
    } as React.CSSProperties,
  })
}

const CHANNEL_ICON_MAP: Record<
  string,
  React.ComponentType<{ className?: string }>
> = {
  telegram: IconBrandTelegram,
  discord: IconBrandDiscord,
  slack: IconBrandSlack,
  feishu: IconLark,
  dingtalk: IconBrandDingtalk,
  line: IconBrandLine,
  qq: IconBrandQq,
  weixin: IconBrandWechat,
  wecom: IconBrandWechat,
  whatsapp: IconBrandWhatsapp,
  whatsapp_native: IconBrandWhatsapp,
  matrix: IconBrandMatrix,
  maixcam: IconCamera,
  onebot: IconRobot,
  web: IconBrandChrome,
  irc: IconMessages,
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

// The browser chat is itself the "web" channel: turning it off here would
// cut off this very chat, so it is not listed with the chat apps.
const HIDDEN_CHANNELS = new Set(["web"])

function isChannelEnabled(
  channel: SupportedChannel,
  channelsConfig: Record<string, unknown>,
): boolean {
  const channelConfig = asRecord(channelsConfig[channel.config_key])
  if (channelConfig.enabled !== true) {
    return false
  }

  // whatsapp / whatsapp_native share one config block and are split by
  // use_native, which the config keeps with the channel's settings.
  const useNative =
    asRecord(channelConfig.settings).use_native ?? channelConfig.use_native
  if (channel.name === "whatsapp_native") {
    return useNative === true
  }
  if (channel.name === "whatsapp") {
    return useNative !== true
  }

  return true
}

function buildChannelEnabledMap(
  channels: SupportedChannel[],
  appConfig: AppConfig,
): Record<string, boolean> {
  // GET /api/config keeps the channels under channel_list, by config key.
  const channelsConfig = asRecord(asRecord(appConfig).channel_list)
  const result: Record<string, boolean> = {}
  for (const channel of channels) {
    result[channel.name] = isChannelEnabled(channel, channelsConfig)
  }
  return result
}

export interface ChannelListItem {
  key: string
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
  enabled: boolean
}

interface UseChannelListOptions {
  language: string
  t: TFunction
}

/**
 * Every chat app channel the gateway supports, enabled ones first and then
 * by how widely each is used in the UI language's region. The browser chat's
 * own channel is left out.
 */
export function useChannelList({ language, t }: UseChannelListOptions) {
  const gateway = useAtomValue(gatewayAtom)
  const [channels, setChannels] = React.useState<SupportedChannel[]>([])
  const [enabledMap, setEnabledMap] = React.useState<Record<string, boolean>>(
    {},
  )
  const [loading, setLoading] = React.useState(true)
  const [error, setError] = React.useState("")

  const reloadChannels = React.useCallback((shouldApply?: () => boolean) => {
    Promise.all([
      getChannelsCatalog(),
      getAppConfig().catch(() => ({}) as AppConfig),
    ])
      .then(([catalog, appConfig]) => {
        if (shouldApply && !shouldApply()) {
          return
        }
        const listed = catalog.channels.filter(
          (channel) => !HIDDEN_CHANNELS.has(channel.name),
        )
        setChannels(listed)
        setEnabledMap(buildChannelEnabledMap(listed, appConfig))
        setError("")
      })
      .catch((cause: unknown) => {
        if (shouldApply && !shouldApply()) {
          return
        }
        setChannels([])
        setEnabledMap({})
        setError(cause instanceof Error ? cause.message : String(cause))
      })
      .finally(() => {
        if (!shouldApply || shouldApply()) setLoading(false)
      })
  }, [])

  React.useEffect(() => {
    let active = true
    reloadChannels(() => active)
    return () => {
      active = false
    }
  }, [reloadChannels])

  const previousGatewayStatusRef = React.useRef(gateway.status)
  React.useEffect(() => {
    const previousStatus = previousGatewayStatusRef.current
    if (previousStatus !== "running" && gateway.status === "running") {
      reloadChannels()
    }
    previousGatewayStatusRef.current = gateway.status
  }, [gateway.status, reloadChannels])

  const channelImportanceIndex = React.useMemo(() => {
    return new Map(
      getChannelImportanceOrder(language).map((name, index) => [name, index]),
    )
  }, [language])

  const items = React.useMemo<ChannelListItem[]>(() => {
    const list = [...channels]
    list.sort((a, b) => {
      const aEnabled = enabledMap[a.name] === true
      const bEnabled = enabledMap[b.name] === true
      if (aEnabled !== bEnabled) {
        return aEnabled ? -1 : 1
      }

      const aImportance =
        channelImportanceIndex.get(a.name) ?? Number.MAX_SAFE_INTEGER
      const bImportance =
        channelImportanceIndex.get(b.name) ?? Number.MAX_SAFE_INTEGER
      if (aImportance !== bImportance) {
        return aImportance - bImportance
      }

      return getChannelDisplayName(a, t).localeCompare(
        getChannelDisplayName(b, t),
      )
    })
    return list.map((channel) => ({
      key: channel.name,
      title: getChannelDisplayName(channel, t),
      url: `/channels/${channel.name}`,
      icon: CHANNEL_ICON_MAP[channel.name] ?? IconPlug,
      enabled: enabledMap[channel.name] === true,
    }))
  }, [channelImportanceIndex, channels, enabledMap, t])

  return { channels: items, loading, error }
}
