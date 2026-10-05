import { IconPhoto } from "@tabler/icons-react"
import { useQuery } from "@tanstack/react-query"
import { type ComponentProps, useState } from "react"

import { getLauncherConfig } from "@/api/system"
import { Button } from "@/components/ui/button"
import { isRemoteURL } from "@/lib/safe-url"

type MarkdownElementProps<T extends "a" | "img"> = ComponentProps<T> & {
  node?: unknown
}

/** How remote images in markdown load; "click" unless the launcher says. */
function useRemoteImageMode(): "click" | "always" {
  const { data } = useQuery({
    queryKey: ["system", "launcher-config"],
    queryFn: getLauncherConfig,
    staleTime: 5 * 60 * 1000,
  })
  return data?.remote_images === "always" ? "always" : "click"
}

// Images the user chose to load stay loaded when a streamed reply re-renders.
const revealedRemoteImages = new Set<string>()

/**
 * An image from another site loads only when clicked: fetching it tells that
 * site the reply was read, and the reply may come from a model, a tool or a
 * skill rather than from the user.
 */
export function MarkdownImage({
  src,
  alt,
  title,
  width,
  height,
}: MarkdownElementProps<"img">) {
  const mode = useRemoteImageMode()
  const source = typeof src === "string" ? src : ""
  const [revealed, setRevealed] = useState(() =>
    revealedRemoteImages.has(source),
  )

  if (!source) return null

  if (mode === "click" && !revealed && isRemoteURL(source)) {
    let label = alt || source
    if (!alt) {
      try {
        label = new URL(source, window.location.href).hostname
      } catch {
        // Keep the address as the label.
      }
    }
    return (
      <Button
        type="button"
        variant="outline"
        size="sm"
        className="max-w-full"
        title={source}
        onClick={() => {
          revealedRemoteImages.add(source)
          setRevealed(true)
        }}
      >
        <IconPhoto className="size-4" />
        <span className="truncate">{label}</span>
      </Button>
    )
  }

  return (
    <img
      src={source}
      alt={alt ?? ""}
      title={title}
      width={width}
      height={height}
      referrerPolicy="no-referrer"
    />
  )
}

/**
 * Links in a reply open in a new tab: following one in place would leave
 * the chat and drop a reply that is still streaming.
 */
export function MarkdownLink({
  href,
  title,
  children,
}: MarkdownElementProps<"a">) {
  if (!href || href.startsWith("#")) {
    return (
      <a href={href} title={title}>
        {children}
      </a>
    )
  }

  return (
    <a href={href} title={title} target="_blank" rel="noopener noreferrer">
      {children}
    </a>
  )
}
