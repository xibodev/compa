import {
  IconAtom,
  IconChevronRight,
  IconListDetails,
  IconMessageCircle,
  IconMessages,
  IconMicrophone,
  IconPlug,
  IconSearch,
  IconSettings,
  IconSparkles,
  IconTools,
} from "@tabler/icons-react"
import { Link, useRouterState } from "@tanstack/react-router"
import * as React from "react"
import { useTranslation } from "react-i18next"

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarRail,
  useSidebar,
} from "@/components/ui/sidebar"
import { activeNavUrl } from "@/lib/nav"

interface NavItem {
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
  tour?: string
}

interface NavGroup {
  label: string
  items: NavItem[]
}

// Pages everyone uses sit at the top level; the rest are grouped.
const TOP_ITEMS: NavItem[] = [
  { title: "navigation.chat", url: "/", icon: IconMessageCircle },
  {
    title: "navigation.models",
    url: "/models",
    icon: IconAtom,
    tour: "models-nav",
  },
  { title: "navigation.channels", url: "/channels", icon: IconMessages },
]

const NAV_GROUPS: NavGroup[] = [
  {
    label: "navigation.agent_group",
    items: [
      { title: "navigation.hub", url: "/agent/hub", icon: IconSearch },
      { title: "navigation.skills", url: "/agent/skills", icon: IconSparkles },
      { title: "navigation.modules", url: "/agent/modules", icon: IconPlug },
      { title: "navigation.tools", url: "/agent/tools", icon: IconTools },
    ],
  },
  {
    label: "navigation.services",
    items: [
      { title: "navigation.config", url: "/config", icon: IconSettings },
      { title: "navigation.voice", url: "/config/voice", icon: IconMicrophone },
      { title: "navigation.logs", url: "/logs", icon: IconListDetails },
    ],
  },
]

const ALL_URLS = [
  ...TOP_ITEMS.map((item) => item.url),
  ...NAV_GROUPS.flatMap((group) => group.items.map((item) => item.url)),
]

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const pathname = useRouterState({ select: (s) => s.location.pathname })
  const { t } = useTranslation()
  const { isMobile, setOpenMobile } = useSidebar()
  const activeUrl = activeNavUrl(pathname, ALL_URLS)

  const handleNavItemClick = React.useCallback(() => {
    if (isMobile) {
      setOpenMobile(false)
    }
  }, [isMobile, setOpenMobile])

  const renderItem = (item: NavItem) => {
    const isActive = item.url === activeUrl
    return (
      <SidebarMenuItem key={item.url}>
        <SidebarMenuButton
          asChild
          isActive={isActive}
          onClick={handleNavItemClick}
          data-tour={item.tour}
          className={`h-9 px-3 ${isActive ? "bg-accent/80 text-foreground font-medium" : "text-muted-foreground hover:bg-muted/60"}`}
        >
          {/* The router's own matching would also mark /config active on
              /config/voice; the longest-match entry above is the one. */}
          <Link
            to={item.url}
            activeOptions={{ exact: true }}
            aria-current={isActive ? "page" : undefined}
          >
            <item.icon
              className={`size-4 ${isActive ? "opacity-100" : "opacity-60"}`}
            />
            <span className={isActive ? "opacity-100" : "opacity-80"}>
              {t(item.title)}
            </span>
          </Link>
        </SidebarMenuButton>
      </SidebarMenuItem>
    )
  }

  return (
    <Sidebar
      {...props}
      className="bg-background border-r-border/20 border-r pt-3"
    >
      <SidebarContent className="bg-background">
        <nav aria-label={t("navigation.label")} className="contents">
          <SidebarGroup className="px-2 py-0">
            <SidebarGroupContent>
              <SidebarMenu>{TOP_ITEMS.map(renderItem)}</SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>

          {NAV_GROUPS.map((group) => (
            <Collapsible
              key={group.label}
              defaultOpen
              className="group/collapsible mt-2 mb-1"
            >
              <SidebarGroup className="px-2 py-0">
                <SidebarGroupLabel asChild>
                  <CollapsibleTrigger className="hover:bg-muted/60 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-1.5 transition-colors">
                    <span>{t(group.label)}</span>
                    <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90" />
                  </CollapsibleTrigger>
                </SidebarGroupLabel>
                <CollapsibleContent>
                  <SidebarGroupContent className="pt-1">
                    <SidebarMenu>{group.items.map(renderItem)}</SidebarMenu>
                  </SidebarGroupContent>
                </CollapsibleContent>
              </SidebarGroup>
            </Collapsible>
          ))}
        </nav>
      </SidebarContent>
      <SidebarRail />
    </Sidebar>
  )
}
