import { IconChevronRight, IconLoader2 } from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"
import { useTranslation } from "react-i18next"

import { PageHeader } from "@/components/page-header"
import { useChannelList } from "@/hooks/use-channel-list"
import { cn } from "@/lib/utils"

/**
 * Every chat app Compa can be reached from. Chat in this browser needs none
 * of them, so they live on one page instead of crowding the navigation.
 */
export function ChannelsPage() {
  const { i18n, t } = useTranslation()
  const { channels, loading, error } = useChannelList({
    language: (i18n.resolvedLanguage ?? i18n.language ?? "").toLowerCase(),
    t,
  })

  return (
    <div className="flex h-full flex-col">
      <PageHeader title={t("navigation.channels")} />
      <div className="min-h-0 flex-1 overflow-y-auto px-4 pb-8 sm:px-6">
        <div className="mx-auto w-full max-w-4xl space-y-5 pt-4">
          <p className="text-muted-foreground max-w-2xl text-sm">
            {t("channels.list.description")}
          </p>
          {loading ? (
            <div className="flex justify-center py-16">
              <IconLoader2
                className="text-muted-foreground size-6 animate-spin"
                aria-label={t("labels.loading")}
              />
            </div>
          ) : error ? (
            <p role="alert" className="text-destructive text-sm">
              {t("channels.loadError")}: {error}
            </p>
          ) : (
            <ul className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
              {channels.map((channel) => (
                <li key={channel.key}>
                  <Link
                    to="/channels/$name"
                    params={{ name: channel.key }}
                    className="border-border/70 bg-card hover:bg-muted/40 focus-visible:ring-ring/50 flex items-center gap-3 rounded-xl border px-4 py-3 transition-colors outline-none focus-visible:ring-3"
                  >
                    <span className="bg-muted text-foreground/80 flex size-9 shrink-0 items-center justify-center rounded-lg">
                      <channel.icon className="size-4.5" />
                    </span>
                    <span className="min-w-0 flex-1">
                      <span className="text-foreground block truncate text-sm font-medium">
                        {channel.title}
                      </span>
                      <span
                        className={cn(
                          "text-xs",
                          channel.enabled
                            ? "text-emerald-600 dark:text-emerald-400"
                            : "text-muted-foreground",
                        )}
                      >
                        {channel.enabled
                          ? t("channels.list.enabled")
                          : t("channels.list.disabled")}
                      </span>
                    </span>
                    <IconChevronRight className="text-muted-foreground size-4 shrink-0" />
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </div>
  )
}
